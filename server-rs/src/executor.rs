//! Job executor for the BTB Service.
//!
//! Manages the full lifecycle of a single btb run: clone the repository,
//! optionally continue from a prior retry's results branch, run btb inside
//! `script` for terminal capture, push results back, preserve logs, and
//! clean up the working directory.

use crate::models::{Job, PushResult};
use crate::pusher::ResultPusher;
use crate::queue::JobQueue;
use std::path::{Path, PathBuf};
use std::process::Stdio;
use std::sync::Arc;
use tokio::process::Command;
use tokio::sync::Mutex;
use tracing::{error, info, warn};

/// Grace period (seconds) between SIGTERM and SIGKILL on timeout.
const KILL_GRACE_PERIOD: u64 = 10;

/// Executes a single btb job with full lifecycle management.
pub struct JobExecutor {
    btb_path: String,
    jobs_dir: PathBuf,
    logs_dir: PathBuf,
    pusher: Arc<ResultPusher>,
    queue: Arc<Mutex<JobQueue>>,
    job_timeout: u64,
    github_token: Option<String>,
    running: Arc<tokio::sync::watch::Sender<bool>>,
    running_rx: tokio::sync::watch::Receiver<bool>,
    current_job: Arc<Mutex<Option<Job>>>,
    stop_requested: Arc<std::sync::atomic::AtomicBool>,
}

impl JobExecutor {
    pub fn new(
        btb_path: &str,
        jobs_dir: &str,
        logs_dir: &str,
        pusher: Arc<ResultPusher>,
        queue: Arc<Mutex<JobQueue>>,
        job_timeout: u64,
        github_token: Option<&str>,
    ) -> Self {
        let (running_tx, running_rx) = tokio::sync::watch::channel(false);
        Self {
            btb_path: btb_path.to_string(),
            jobs_dir: PathBuf::from(jobs_dir),
            logs_dir: PathBuf::from(logs_dir),
            pusher,
            queue,
            job_timeout,
            github_token: github_token.map(|s| s.to_string()),
            running: Arc::new(running_tx),
            running_rx,
            current_job: Arc::new(Mutex::new(None)),
            stop_requested: Arc::new(std::sync::atomic::AtomicBool::new(false)),
        }
    }

    pub fn is_running(&self) -> bool {
        *self.running_rx.borrow()
    }

    pub async fn get_current_job(&self) -> Option<Job> {
        self.current_job.lock().await.clone()
    }

    pub fn request_stop(&self) -> bool {
        if !self.is_running() {
            return false;
        }
        self.stop_requested
            .store(true, std::sync::atomic::Ordering::SeqCst);
        true
    }

    fn job_dir(&self, job: &Job) -> PathBuf {
        self.jobs_dir.join(&job.id)
    }

    fn repo_dir(&self, job: &Job) -> PathBuf {
        self.job_dir(job).join("repo")
    }

    fn typescript_path(&self, job: &Job) -> PathBuf {
        self.job_dir(job).join("typescript.log")
    }

    /// Run a subprocess and return (return_code, stdout, stderr).
    async fn run_subprocess(
        &self,
        args: &[&str],
        cwd: &str,
        timeout: Option<u64>,
    ) -> (i32, String, String) {
        let cmd = &args[0];
        let cmd_args = &args[1..];

        let output_future = Command::new(cmd)
            .args(cmd_args)
            .current_dir(cwd)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .output();

        let output = if let Some(timeout_secs) = timeout {
            match tokio::time::timeout(
                std::time::Duration::from_secs(timeout_secs),
                output_future,
            )
            .await
            {
                Ok(result) => result,
                Err(_) => return (-1, String::new(), "Command timed out".to_string()),
            }
        } else {
            output_future.await
        };

        match output {
            Ok(out) => {
                let stdout = String::from_utf8_lossy(&out.stdout).trim().to_string();
                let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
                let code = out.status.code().unwrap_or(-1);
                (code, stdout, stderr)
            }
            Err(e) => (-1, String::new(), format!("Failed to execute: {}", e)),
        }
    }

    /// Get the authenticated URL for cloning.
    fn get_authenticated_url(&self, repo_url: &str) -> String {
        match &self.github_token {
            Some(token) if repo_url.starts_with("https://github.com/") => repo_url.replacen(
                "https://github.com/",
                &format!("https://x-access-token:{}@github.com/", token),
                1,
            ),
            _ => repo_url.to_string(),
        }
    }

    /// Clone the repository and checkout the target commit.
    async fn clone_repo(&self, job: &Job) -> i32 {
        let repo_dir = self.repo_dir(job);
        let job_dir = self.job_dir(job);
        let _ = std::fs::create_dir_all(&job_dir);

        let clone_url = self.get_authenticated_url(&job.repo_url);
        let repo_dir_str = repo_dir.to_string_lossy().to_string();
        let job_dir_str = job_dir.to_string_lossy().to_string();

        let (rc, _, err) = self
            .run_subprocess(
                &[
                    "git",
                    "clone",
                    "--branch",
                    &job.branch,
                    &clone_url,
                    &repo_dir_str,
                ],
                &job_dir_str,
                Some(300),
            )
            .await;

        if rc != 0 {
            error!("git clone failed for job {}: {}", job.id, err);
            return rc;
        }

        let (rc, _, err) = self
            .run_subprocess(
                &["git", "checkout", &job.commit_sha],
                &repo_dir_str,
                Some(60),
            )
            .await;

        if rc != 0 {
            error!("git checkout failed for job {}: {}", job.id, err);
            return rc;
        }

        0
    }

    /// Handle retry continuation - fetch and merge prior btb work.
    async fn handle_retry_continuation(&self, job: &Job) {
        let retry_of = match &job.retry_of {
            Some(r) => r.clone(),
            None => return,
        };

        let repo_dir = self.repo_dir(job);
        let repo_dir_str = repo_dir.to_string_lossy().to_string();
        let results_branch = format!("btb-results/{}", job.branch);

        // Check if the results branch exists
        let (rc, out, _) = self
            .run_subprocess(
                &["git", "ls-remote", "--heads", "origin", &results_branch],
                &repo_dir_str,
                Some(60),
            )
            .await;

        if rc != 0 || out.trim().is_empty() {
            info!(
                "No results branch {} found for retry job {} - starting fresh",
                results_branch, job.id
            );
            return;
        }

        // Fetch the results branch
        let (rc, _, err) = self
            .run_subprocess(
                &["git", "fetch", "origin", &results_branch],
                &repo_dir_str,
                Some(120),
            )
            .await;

        if rc != 0 {
            warn!(
                "Failed to fetch results branch {} for job {}: {} - starting fresh",
                results_branch, job.id, err
            );
            return;
        }

        // Merge the results branch
        let (rc, _, err) = self
            .run_subprocess(
                &[
                    "git",
                    "merge",
                    "FETCH_HEAD",
                    "--no-edit",
                    "--allow-unrelated-histories",
                ],
                &repo_dir_str,
                Some(60),
            )
            .await;

        if rc != 0 {
            warn!(
                "Failed to merge results branch for job {}: {} - starting fresh",
                job.id, err
            );
            let _ = self
                .run_subprocess(&["git", "merge", "--abort"], &repo_dir_str, Some(30))
                .await;
            return;
        }

        info!(
            "Merged prior btb work from {} for retry job {}",
            results_branch, job.id
        );
    }

    /// Run btb's setup.sh to install agents.
    async fn run_setup(&self, job: &Job) -> i32 {
        let setup_script = format!("{}/setup.sh", self.btb_path);
        let repo_dir = self.repo_dir(job);
        let repo_dir_str = repo_dir.to_string_lossy().to_string();

        let (rc, _, err) = self
            .run_subprocess(&[&setup_script], &repo_dir_str, Some(120))
            .await;

        if rc != 0 {
            error!("setup.sh failed for job {}: {}", job.id, err);
        }
        rc
    }

    /// Run btb inside `script` for terminal capture.
    async fn run_btb(&self, job: &Job) -> i32 {
        let repo_dir = self.repo_dir(job);
        let typescript_path = self.typescript_path(job);
        let btb_script = format!("{}/btb.sh", self.btb_path);
        let repo_dir_str = repo_dir.to_string_lossy().to_string();
        let typescript_str = typescript_path.to_string_lossy().to_string();

        // Linux: script -q -c "btb.sh <spec>" typescript.log
        let shell_cmd = format!(
            "script -q -c \"{} {}\" {}",
            btb_script, job.spec_name, typescript_str
        );

        let mut child = match Command::new("bash")
            .args(["-c", &shell_cmd])
            .current_dir(&repo_dir_str)
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .process_group(0) // New process group
            .spawn()
        {
            Ok(c) => c,
            Err(e) => {
                error!("Failed to spawn btb process for job {}: {}", job.id, e);
                return -1;
            }
        };

        let pid = child.id();

        // Wait with timeout
        match tokio::time::timeout(
            std::time::Duration::from_secs(self.job_timeout),
            child.wait(),
        )
        .await
        {
            Ok(Ok(status)) => status.code().unwrap_or(0),
            Ok(Err(e)) => {
                error!("Error waiting for btb process: {}", e);
                -1
            }
            Err(_) => {
                warn!(
                    "Job {} timed out after {} seconds - sending SIGTERM",
                    job.id, self.job_timeout
                );
                // Send SIGTERM to process group
                if let Some(pid) = pid {
                    unsafe {
                        libc::killpg(pid as i32, libc::SIGTERM);
                    }
                }

                // Wait grace period
                match tokio::time::timeout(
                    std::time::Duration::from_secs(KILL_GRACE_PERIOD),
                    child.wait(),
                )
                .await
                {
                    Ok(_) => {}
                    Err(_) => {
                        warn!(
                            "Job {} did not exit after SIGTERM - sending SIGKILL",
                            job.id
                        );
                        if let Some(pid) = pid {
                            unsafe {
                                libc::killpg(pid as i32, libc::SIGKILL);
                            }
                        }
                        let _ = child.wait().await;
                    }
                }
                -1
            }
        }
    }

    /// Preserve .ralph-logs/ to the persistent logs directory.
    fn preserve_logs(&self, job: &Job) -> bool {
        let repo_dir = self.repo_dir(job);
        let ralph_logs = repo_dir.join(".ralph-logs");
        let dest = self.logs_dir.join(&job.id);

        if !ralph_logs.exists() {
            info!("No .ralph-logs/ found for job {}", job.id);
            return true;
        }

        match copy_dir_all(&ralph_logs, &dest) {
            Ok(_) => {
                info!("Preserved logs for job {} to {:?}", job.id, dest);
                true
            }
            Err(e) => {
                error!("Failed to preserve logs for job {}: {}", job.id, e);
                false
            }
        }
    }

    /// Delete the entire job working directory.
    fn cleanup_workdir(&self, job: &Job) -> bool {
        let job_dir = self.job_dir(job);
        if !job_dir.exists() {
            return true;
        }

        match std::fs::remove_dir_all(&job_dir) {
            Ok(_) => {
                info!("Cleaned up working directory for job {}", job.id);
                true
            }
            Err(e) => {
                error!(
                    "Failed to clean up working directory for job {}: {}",
                    job.id, e
                );
                false
            }
        }
    }

    /// Execute a full job lifecycle.
    pub async fn run(&self, job: &Job) -> i32 {
        if self.is_running() {
            error!("Another job is already running");
            return -1;
        }

        let _ = self.running.send(true);
        *self.current_job.lock().await = Some(job.clone());
        self.stop_requested
            .store(false, std::sync::atomic::Ordering::SeqCst);

        let mut exit_code: i32 = -1;
        let mut status = "failed".to_string();
        let mut error_msg: Option<String> = None;
        let mut push_result: Option<PushResult> = None;
        let mut cleanup_success = false;

        // Step 1: Clean stale working directory
        let job_dir = self.job_dir(job);
        if job_dir.exists() {
            warn!(
                "Stale working directory found for job {} - deleting",
                job.id
            );
            let _ = std::fs::remove_dir_all(&job_dir);
        }

        // Step 2: Clone repo
        let clone_rc = self.clone_repo(job).await;
        if clone_rc != 0 {
            exit_code = clone_rc;
            error_msg = Some("Git clone or checkout failed".to_string());
            status = "failed".to_string();

            if self.repo_dir(job).exists() {
                push_result = Some(
                    self.pusher
                        .push_results(job, &self.repo_dir(job).to_string_lossy(), "failed", &job.id, None)
                        .await,
                );
            }
            self.preserve_logs(job);
            cleanup_success = self.cleanup_workdir(job);
            self.complete_job(job, &status, exit_code, error_msg.as_deref(), push_result.as_ref(), cleanup_success)
                .await;
            let _ = self.running.send(false);
            *self.current_job.lock().await = None;
            return exit_code;
        }

        // Step 3: Handle retry continuation
        self.handle_retry_continuation(job).await;

        // Step 4: Run setup.sh
        let setup_rc = self.run_setup(job).await;
        if setup_rc != 0 {
            exit_code = setup_rc;
            error_msg = Some("setup.sh failed".to_string());
            status = "failed".to_string();

            push_result = Some(
                self.pusher
                    .push_results(job, &self.repo_dir(job).to_string_lossy(), "failed", &job.id, None)
                    .await,
            );
            self.preserve_logs(job);
            cleanup_success = self.cleanup_workdir(job);
            self.complete_job(job, &status, exit_code, error_msg.as_deref(), push_result.as_ref(), cleanup_success)
                .await;
            let _ = self.running.send(false);
            *self.current_job.lock().await = None;
            return exit_code;
        }

        // Step 5: Run btb
        exit_code = self.run_btb(job).await;

        if exit_code == -1 {
            status = "timeout".to_string();
            error_msg = Some(format!("Job timed out after {} seconds", self.job_timeout));
        } else if exit_code == 0 {
            status = "completed".to_string();
        } else {
            status = "failed".to_string();
            error_msg = Some(format!("btb exited with code {}", exit_code));
        }

        // Steps 6-9: Post-execution lifecycle
        let repo_dir_str = self.repo_dir(job).to_string_lossy().to_string();
        if self.repo_dir(job).exists() {
            push_result = Some(
                self.pusher
                    .push_results(job, &repo_dir_str, &status, &job.id, None)
                    .await,
            );

            // Push to source branch (completed only)
            if status == "completed" {
                let source_result = self.pusher.push_to_source_branch(job, &repo_dir_str).await;
                if !source_result.success {
                    warn!(
                        "Source push failed for job {}: {:?}",
                        job.id, source_result.error
                    );
                }
            }
        } else {
            push_result = Some(PushResult {
                success: false,
                branch: format!("btb-results/{}", job.branch),
                error: Some("Repo directory does not exist".to_string()),
            });
        }

        // Step 7: Preserve logs
        self.preserve_logs(job);

        // Step 8: Cleanup working directory
        cleanup_success = self.cleanup_workdir(job);

        // Step 9: Complete the job
        self.complete_job(job, &status, exit_code, error_msg.as_deref(), push_result.as_ref(), cleanup_success)
            .await;

        let _ = self.running.send(false);
        *self.current_job.lock().await = None;

        exit_code
    }

    async fn complete_job(
        &self,
        job: &Job,
        status: &str,
        exit_code: i32,
        error: Option<&str>,
        push_result: Option<&PushResult>,
        cleanup_success: bool,
    ) {
        let queue = self.queue.lock().await;
        if let Err(e) = queue.complete(
            &job.id,
            status,
            exit_code,
            error,
            push_result.map(|pr| pr.branch.as_str()),
            push_result.map(|pr| pr.success),
            push_result.and_then(|pr| pr.error.as_deref()),
            Some(cleanup_success),
        ) {
            error!("Failed to complete job {} in queue: {}", job.id, e);
        }
    }
}

/// Recursively copy a directory.
fn copy_dir_all(src: &Path, dst: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dst)?;
    for entry in std::fs::read_dir(src)? {
        let entry = entry?;
        let ty = entry.file_type()?;
        if ty.is_dir() {
            copy_dir_all(&entry.path(), &dst.join(entry.file_name()))?;
        } else {
            std::fs::copy(entry.path(), dst.join(entry.file_name()))?;
        }
    }
    Ok(())
}
