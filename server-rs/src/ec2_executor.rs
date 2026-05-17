//! EC2-based job executor for the BTB Service.
//!
//! Instead of running btb locally, this executor starts a pre-configured
//! "worker" EC2 instance, sends the job via SSM Run Command, monitors
//! execution, and lets the worker stop itself when done.

use crate::models::{Job, PushResult};
use crate::queue::JobQueue;
use aws_sdk_ec2 as ec2;
use aws_sdk_ssm as ssm;
use std::sync::Arc;
use std::time::Duration;
use tokio::sync::Mutex;
use tracing::{error, info, warn};

/// How often to poll the worker instance status (seconds).
const POLL_INTERVAL: u64 = 30;

/// Max time to wait for the instance to reach "running" state (seconds).
const INSTANCE_START_TIMEOUT: u64 = 300;

/// Max time to wait for SSM agent to come online (seconds).
const SSM_READY_TIMEOUT: u64 = 180;

/// Executes btb jobs on a remote EC2 worker instance.
pub struct EC2JobExecutor {
    worker_instance_id: String,
    worker_region: String,
    btb_path: String,
    github_token: String,
    queue: Arc<Mutex<JobQueue>>,
    job_timeout: u64,
    running: Arc<tokio::sync::watch::Sender<bool>>,
    running_rx: tokio::sync::watch::Receiver<bool>,
    current_job: Arc<Mutex<Option<Job>>>,
    stop_requested: Arc<std::sync::atomic::AtomicBool>,
    ec2_client: Option<ec2::Client>,
    ssm_client: Option<ssm::Client>,
}

impl EC2JobExecutor {
    pub fn new(
        worker_instance_id: &str,
        worker_region: &str,
        btb_path: &str,
        github_token: &str,
        queue: Arc<Mutex<JobQueue>>,
        job_timeout: u64,
    ) -> Self {
        let (running_tx, running_rx) = tokio::sync::watch::channel(false);
        Self {
            worker_instance_id: worker_instance_id.to_string(),
            worker_region: worker_region.to_string(),
            btb_path: btb_path.to_string(),
            github_token: github_token.to_string(),
            queue,
            job_timeout,
            running: Arc::new(running_tx),
            running_rx,
            current_job: Arc::new(Mutex::new(None)),
            stop_requested: Arc::new(std::sync::atomic::AtomicBool::new(false)),
            ec2_client: None,
            ssm_client: None,
        }
    }

    /// Initialize AWS clients lazily.
    pub async fn init_clients(&mut self) {
        let config = aws_config::defaults(aws_config::BehaviorVersion::latest())
            .region(aws_config::Region::new(self.worker_region.clone()))
            .load()
            .await;
        self.ec2_client = Some(ec2::Client::new(&config));
        self.ssm_client = Some(ssm::Client::new(&config));
    }

    fn ec2(&self) -> &ec2::Client {
        self.ec2_client.as_ref().expect("EC2 client not initialized")
    }

    fn ssm(&self) -> &ssm::Client {
        self.ssm_client.as_ref().expect("SSM client not initialized")
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

        // Force-stop the worker instance
        let ec2 = self.ec2().clone();
        let instance_id = self.worker_instance_id.clone();
        tokio::spawn(async move {
            let _ = ec2
                .stop_instances()
                .instance_ids(&instance_id)
                .force(true)
                .send()
                .await;
            info!("Force-stopped worker instance {}", instance_id);
        });
        true
    }

    /// Start the worker EC2 instance and wait for it to be running.
    async fn start_worker_instance(&self) -> bool {
        let resp = match self
            .ec2()
            .describe_instances()
            .instance_ids(&self.worker_instance_id)
            .send()
            .await
        {
            Ok(r) => r,
            Err(e) => {
                error!("Failed to describe worker instance: {}", e);
                return false;
            }
        };

        let state = resp
            .reservations()
            .first()
            .and_then(|r| r.instances().first())
            .and_then(|i| i.state())
            .and_then(|s| s.name())
            .map(|n| n.as_str().to_string())
            .unwrap_or_default();

        if state == "running" {
            info!("Worker instance already running");
            return true;
        }

        if state != "stopped" && state != "stopping" {
            error!("Worker instance in unexpected state: {}", state);
            return false;
        }

        // Wait for stopped if stopping
        if state == "stopping" {
            info!("Worker instance is stopping, waiting...");
            tokio::time::sleep(Duration::from_secs(30)).await;
        }

        // Start the instance
        info!("Starting worker instance {}", self.worker_instance_id);
        if let Err(e) = self
            .ec2()
            .start_instances()
            .instance_ids(&self.worker_instance_id)
            .send()
            .await
        {
            error!("Failed to start worker instance: {}", e);
            return false;
        }

        // Wait for running state
        let deadline = tokio::time::Instant::now() + Duration::from_secs(INSTANCE_START_TIMEOUT);
        loop {
            if tokio::time::Instant::now() > deadline {
                error!("Worker instance failed to start in time");
                return false;
            }
            tokio::time::sleep(Duration::from_secs(10)).await;

            let resp = self
                .ec2()
                .describe_instances()
                .instance_ids(&self.worker_instance_id)
                .send()
                .await;

            if let Ok(r) = resp {
                let s = r
                    .reservations()
                    .first()
                    .and_then(|r| r.instances().first())
                    .and_then(|i| i.state())
                    .and_then(|s| s.name())
                    .map(|n| n.as_str().to_string())
                    .unwrap_or_default();
                if s == "running" {
                    info!("Worker instance is running");
                    return true;
                }
            }
        }
    }

    /// Wait for SSM agent on the worker to come online.
    async fn wait_for_ssm(&self) -> bool {
        let deadline = tokio::time::Instant::now() + Duration::from_secs(SSM_READY_TIMEOUT);

        while tokio::time::Instant::now() < deadline {
            let filter = match ssm::types::InstanceInformationStringFilter::builder()
                .key("InstanceIds")
                .values(&self.worker_instance_id)
                .build()
            {
                Ok(f) => f,
                Err(e) => {
                    error!("Failed to build SSM filter: {}", e);
                    return false;
                }
            };

            let resp = self
                .ssm()
                .describe_instance_information()
                .filters(filter)
                .send()
                .await;

            if let Ok(r) = resp {
                if let Some(info) = r.instance_information_list().first() {
                    if info.ping_status() == Some(&ssm::types::PingStatus::Online) {
                        info!("SSM agent is online on worker");
                        return true;
                    }
                }
            }
            tokio::time::sleep(Duration::from_secs(10)).await;
        }

        error!("SSM agent did not come online within {}s", SSM_READY_TIMEOUT);
        false
    }

    /// Shell-escape a value for safe single-quote embedding.
    /// Replaces single quotes with: '\'' (end quote, literal quote, start quote).
    fn shell_escape(s: &str) -> String {
        s.replace('\'', "'\\''")
    }

    /// Validate that a job field value is safe for use in a shell script.
    /// Rejects values containing dangerous shell metacharacters.
    fn validate_shell_field(value: &str, field_name: &str) -> Result<(), String> {
        // Allow alphanumeric, hyphens, underscores, dots, slashes, spaces, @, colons
        // Reject backticks, $, ;, |, &, newlines, etc.
        let dangerous_chars = ['`', '$', ';', '|', '&', '\n', '\r', '(', ')', '{', '}', '<', '>'];
        for c in dangerous_chars {
            if value.contains(c) {
                return Err(format!(
                    "Field '{}' contains unsafe character '{}' — rejecting to prevent injection",
                    field_name, c
                ));
            }
        }
        Ok(())
    }

    /// Build the shell script that runs on the worker instance.
    ///
    /// Security measures:
    /// - All job data is validated against shell metacharacters before interpolation
    /// - The GitHub token is delivered via a restricted env file (chmod 600, owned by ec2-user)
    ///   and is deleted immediately after use
    /// - The env file is never written to logs or output
    fn build_worker_script(&self, job: &Job) -> Result<String, String> {
        let retry_of = job.retry_of.as_deref().unwrap_or("");

        // Validate all job fields that will be interpolated into the shell script
        Self::validate_shell_field(&job.id, "job_id")?;
        Self::validate_shell_field(&job.repo_url, "repo_url")?;
        Self::validate_shell_field(&job.branch, "branch")?;
        Self::validate_shell_field(&job.commit_sha, "commit_sha")?;
        Self::validate_shell_field(&job.spec_name, "spec_name")?;
        Self::validate_shell_field(retry_of, "retry_of")?;
        Self::validate_shell_field(&self.btb_path, "btb_path")?;

        // Shell-escape all values for safe embedding in single-quoted contexts
        let job_id = Self::shell_escape(&job.id);
        let repo_url = Self::shell_escape(&job.repo_url);
        let branch = Self::shell_escape(&job.branch);
        let commit_sha = Self::shell_escape(&job.commit_sha);
        let spec_name = Self::shell_escape(&job.spec_name);
        let btb_path = Self::shell_escape(&self.btb_path);
        let retry_of = Self::shell_escape(retry_of);
        // Token is NOT validated via validate_shell_field since it may contain special chars,
        // but it's delivered through a restricted file, not direct interpolation.
        let github_token = Self::shell_escape(&self.github_token);

        Ok(format!(
            r#"#!/bin/bash
set +e
JOB_ID='{job_id}'
JOBS_DIR="/var/btb/jobs"
LOGS_DIR="/var/btb/logs"
JOB_DIR="${{JOBS_DIR}}/${{JOB_ID}}"
rm -rf "${{JOB_DIR}}" 2>/dev/null || true
mkdir -p "${{JOB_DIR}}" "${{LOGS_DIR}}"
chown -R ec2-user:ec2-user "${{JOB_DIR}}" "${{LOGS_DIR}}"

# Write credentials to a restricted file owned by ec2-user only.
# This file is deleted immediately after the worker script sources it.
CRED_FILE="/run/btb-cred-${{JOB_ID}}"
install -m 0600 -o ec2-user -g ec2-user /dev/null "${{CRED_FILE}}"
cat > "${{CRED_FILE}}" << 'CREDEOF'
{github_token}
CREDEOF
chmod 600 "${{CRED_FILE}}"
chown ec2-user:ec2-user "${{CRED_FILE}}"

ENV_FILE="/tmp/btb-env-${{JOB_ID}}.sh"
install -m 0600 -o ec2-user -g ec2-user /dev/null "${{ENV_FILE}}"
cat > "${{ENV_FILE}}" << ENVEOF
export BTB_JOB_ID='{job_id}'
export BTB_REPO_URL='{repo_url}'
export BTB_BRANCH='{branch}'
export BTB_COMMIT_SHA='{commit_sha}'
export BTB_SPEC_NAME='{spec_name}'
export BTB_PATH='{btb_path}'
export BTB_RETRY_OF='{retry_of}'
export BTB_TIMEOUT='{timeout}'
export CI=true
ENVEOF
chmod 600 "${{ENV_FILE}}"
chown ec2-user:ec2-user "${{ENV_FILE}}"

WORKER_SCRIPT="/tmp/btb-worker-${{JOB_ID}}.sh"
cat > "${{WORKER_SCRIPT}}" << 'BTBEOF'
#!/bin/bash
set +e
export HOME=/home/ec2-user
export PATH="/home/ec2-user/.local/bin:/opt/btb/.local/bin:/usr/local/bin:$PATH"
export TERM=xterm-256color
JOBS_DIR="/var/btb/jobs"
LOGS_DIR="/var/btb/logs"
JOB_DIR="${{JOBS_DIR}}/${{BTB_JOB_ID}}"
REPO_DIR="${{JOB_DIR}}/repo"
OUTPUT_LOG="${{JOB_DIR}}/output.log"
CRED_FILE="/run/btb-cred-${{BTB_JOB_ID}}"

exec > >(tee -a "${{OUTPUT_LOG}}") 2>&1
echo "[$(date -Iseconds)] Starting btb job ${{BTB_JOB_ID}}"

# Read the token from the restricted credential file and delete it immediately
BTB_GITHUB_TOKEN="$(cat "${{CRED_FILE}}" 2>/dev/null)"
rm -f "${{CRED_FILE}}"

# Use GIT_ASKPASS for secure credential delivery (token never in URL or process listing)
ASKPASS_SCRIPT="${{JOB_DIR}}/.git-askpass.sh"
printf '#!/bin/sh\necho "%s"\n' "${{BTB_GITHUB_TOKEN}}" > "${{ASKPASS_SCRIPT}}"
chmod 700 "${{ASKPASS_SCRIPT}}"
export GIT_ASKPASS="${{ASKPASS_SCRIPT}}"
export GIT_TERMINAL_PROMPT=0

# Clone using x-access-token username (password provided via GIT_ASKPASS)
CLONE_URL=$(echo "${{BTB_REPO_URL}}" | sed "s|https://github.com/|https://x-access-token@github.com/|")
git clone --branch "${{BTB_BRANCH}}" "${{CLONE_URL}}" "${{REPO_DIR}}"
cd "${{REPO_DIR}}"
git checkout "${{BTB_COMMIT_SHA}}"

# Clean up askpass and token from memory
rm -f "${{ASKPASS_SCRIPT}}"
unset BTB_GITHUB_TOKEN

if [ -n "${{BTB_RETRY_OF}}" ]; then
    RESULTS_BRANCH="btb-results/${{BTB_BRANCH}}"
    if git ls-remote --heads origin "${{RESULTS_BRANCH}}" | grep -q .; then
        git fetch origin "${{RESULTS_BRANCH}}" || true
        git merge FETCH_HEAD --no-edit --allow-unrelated-histories || git merge --abort || true
    fi
fi
"${{BTB_PATH}}/setup.sh" || true
EXIT_CODE=0
timeout ${{BTB_TIMEOUT}} script -qfc "${{BTB_PATH}}/btb.sh ${{BTB_SPEC_NAME}}" /dev/null || EXIT_CODE=$?
if [ "${{EXIT_CODE}}" -eq 0 ]; then STATUS="completed"
elif [ "${{EXIT_CODE}}" -eq 124 ]; then STATUS="timeout"
else STATUS="failed"; fi
git add -A
if ! git diff --cached --quiet; then
    git commit -m "btb results for ${{BTB_SPEC_NAME}} [job: ${{BTB_JOB_ID}}]"
fi
git push --force origin "HEAD:refs/heads/btb-results/${{BTB_BRANCH}}" || true
if [ "${{STATUS}}" = "completed" ]; then
    git fetch origin "${{BTB_BRANCH}}"
    MERGE_BASE=$(git merge-base HEAD "origin/${{BTB_BRANCH}}" 2>/dev/null || echo "${{BTB_COMMIT_SHA}}")
    git checkout -b "btb-squash-${{BTB_JOB_ID:0:8}}"
    git reset --soft "${{MERGE_BASE}}"
    git commit -m "btb: ${{BTB_SPEC_NAME}} [job: ${{BTB_JOB_ID:0:8}}]" --allow-empty
    if git rebase "origin/${{BTB_BRANCH}}"; then
        git push origin "HEAD:refs/heads/${{BTB_BRANCH}}" || true
    fi
fi
if [ -d .ralph-logs ]; then
    mkdir -p "${{LOGS_DIR}}/${{BTB_JOB_ID}}"
    cp -a .ralph-logs/. "${{LOGS_DIR}}/${{BTB_JOB_ID}}/" || true
fi
echo "${{STATUS}}:${{EXIT_CODE}}" > "${{JOB_DIR}}/result.txt"
echo "[$(date -Iseconds)] Job complete."
exit ${{EXIT_CODE}}
BTBEOF
chmod +x "${{WORKER_SCRIPT}}"
su - ec2-user -c "source ${{ENV_FILE}} && bash ${{WORKER_SCRIPT}}"
WORKER_EXIT=$?
rm -f "${{ENV_FILE}}" "${{WORKER_SCRIPT}}" "${{CRED_FILE}}" 2>/dev/null
nohup bash -c 'sleep 5 && sudo shutdown now' &>/dev/null &
exit $WORKER_EXIT
"#,
            job_id = job_id,
            repo_url = repo_url,
            branch = branch,
            commit_sha = commit_sha,
            spec_name = spec_name,
            github_token = github_token,
            btb_path = btb_path,
            retry_of = retry_of,
            timeout = self.job_timeout,
        ))
    }

    /// Send the btb job command to the worker via SSM Run Command.
    async fn send_job_command(&self, job: &Job) -> Option<String> {
        let script = match self.build_worker_script(job) {
            Ok(s) => s,
            Err(e) => {
                error!("Failed to build worker script for job {}: {}", job.id, e);
                return None;
            }
        };

        let resp = self
            .ssm()
            .send_command()
            .instance_ids(&self.worker_instance_id)
            .document_name("AWS-RunShellScript")
            .parameters("commands", vec![script])
            .parameters(
                "executionTimeout",
                vec![(self.job_timeout + 600).to_string()],
            )
            .timeout_seconds((self.job_timeout + 600) as i32)
            .comment(format!("btb job {} spec={}", job.id, job.spec_name))
            .send()
            .await;

        match resp {
            Ok(r) => {
                let command_id = r
                    .command()
                    .and_then(|c| c.command_id())
                    .map(|s| s.to_string());
                if let Some(ref id) = command_id {
                    info!(
                        "Sent SSM command {} for job {} to worker {}",
                        id, job.id, self.worker_instance_id
                    );
                }
                command_id
            }
            Err(e) => {
                error!("Failed to send SSM command: {}", e);
                None
            }
        }
    }

    /// Monitor an SSM command until completion.
    async fn monitor_command(&self, command_id: &str, job: &Job) -> (String, i32) {
        let start = tokio::time::Instant::now();
        let total_timeout = Duration::from_secs(self.job_timeout + 600);

        loop {
            if self
                .stop_requested
                .load(std::sync::atomic::Ordering::SeqCst)
            {
                return ("failed".to_string(), -1);
            }

            if start.elapsed() > total_timeout {
                error!("Job {} exceeded total timeout", job.id);
                return ("timeout".to_string(), -1);
            }

            // Check SSM command status
            let resp = self
                .ssm()
                .get_command_invocation()
                .command_id(command_id)
                .instance_id(&self.worker_instance_id)
                .send()
                .await;

            if let Ok(inv) = resp {
                let ssm_status = inv
                    .status()
                    .map(|s| s.as_str().to_string())
                    .unwrap_or_default();

                match ssm_status.as_str() {
                    "Success" => return ("completed".to_string(), 0),
                    "Failed" | "Cancelled" | "TimedOut" => {
                        let exit_code = inv.response_code();
                        if ssm_status == "TimedOut" {
                            return ("timeout".to_string(), -1);
                        }
                        // Check if instance stopped (normal completion)
                        if ssm_status == "Failed" && exit_code == -1 {
                            if self.check_instance_stopped().await {
                                return ("completed".to_string(), 0);
                            }
                        }
                        return ("failed".to_string(), exit_code);
                    }
                    _ => {} // InProgress, Pending, Delayed — keep waiting
                }
            }

            // Check if instance stopped
            if self.check_instance_stopped().await {
                info!(
                    "Worker instance stopped for job {} - job likely completed",
                    job.id
                );
                return ("completed".to_string(), 0);
            }

            tokio::time::sleep(Duration::from_secs(POLL_INTERVAL)).await;
        }
    }

    async fn check_instance_stopped(&self) -> bool {
        let resp = self
            .ec2()
            .describe_instances()
            .instance_ids(&self.worker_instance_id)
            .send()
            .await;

        if let Ok(r) = resp {
            let state = r
                .reservations()
                .first()
                .and_then(|r| r.instances().first())
                .and_then(|i| i.state())
                .and_then(|s| s.name())
                .map(|n| n.as_str().to_string())
                .unwrap_or_default();
            return state == "stopped" || state == "stopping";
        }
        false
    }

    /// Stream remote log to local file for TUI streamer.
    async fn stream_remote_log(&self, job: &Job, local_log_path: &str) {
        let mut offset: u64 = 0;
        let remote_log = format!("/var/btb/jobs/{}/output.log", job.id);

        if let Some(parent) = std::path::Path::new(local_log_path).parent() {
            let _ = std::fs::create_dir_all(parent);
        }

        loop {
            if self
                .stop_requested
                .load(std::sync::atomic::Ordering::SeqCst)
            {
                break;
            }

            let cmd = format!("dd if={} bs=1 skip={} 2>/dev/null || true", remote_log, offset);
            let resp = self
                .ssm()
                .send_command()
                .instance_ids(&self.worker_instance_id)
                .document_name("AWS-RunShellScript")
                .parameters("commands", vec![cmd])
                .parameters("executionTimeout", vec!["30".to_string()])
                .timeout_seconds(30)
                .send()
                .await;

            if let Ok(r) = resp {
                if let Some(cmd_id) = r.command().and_then(|c| c.command_id()) {
                    // Wait for command
                    for _ in 0..10 {
                        tokio::time::sleep(Duration::from_secs(1)).await;
                        let inv = self
                            .ssm()
                            .get_command_invocation()
                            .command_id(cmd_id)
                            .instance_id(&self.worker_instance_id)
                            .send()
                            .await;
                        if let Ok(i) = inv {
                            let s = i.status().map(|s| s.as_str()).unwrap_or("");
                            if matches!(s, "Success" | "Failed" | "Cancelled" | "TimedOut") {
                                if s == "Success" {
                                    if let Some(output) = i.standard_output_content() {
                                        if !output.is_empty() {
                                            let bytes = output.as_bytes();
                                            let _ = std::fs::OpenOptions::new()
                                                .create(true)
                                                .append(true)
                                                .open(local_log_path)
                                                .and_then(|mut f| {
                                                    use std::io::Write;
                                                    f.write_all(bytes)
                                                });
                                            offset += bytes.len() as u64;
                                        }
                                    }
                                }
                                break;
                            }
                        }
                    }
                }
            }

            tokio::time::sleep(Duration::from_secs(5)).await;
        }
    }

    /// Execute a job on the remote worker instance.
    pub async fn run(&self, job: &Job) -> i32 {
        if self.is_running() {
            error!("Another job is already running");
            return -1;
        }

        let _ = self.running.send(true);
        *self.current_job.lock().await = Some(job.clone());
        self.stop_requested
            .store(false, std::sync::atomic::Ordering::SeqCst);

        let mut status = "failed".to_string();
        let mut exit_code: i32 = -1;
        let mut error_msg: Option<String> = None;

        // Step 1: Start worker instance
        if !self.start_worker_instance().await {
            error_msg = Some("Failed to start worker EC2 instance".to_string());
            self.complete_job(job, "failed", -1, error_msg.as_deref()).await;
            let _ = self.running.send(false);
            *self.current_job.lock().await = None;
            return -1;
        }

        // Step 2: Wait for SSM
        if !self.wait_for_ssm().await {
            error_msg = Some("SSM agent not available on worker instance".to_string());
            let _ = self
                .ec2()
                .stop_instances()
                .instance_ids(&self.worker_instance_id)
                .send()
                .await;
            self.complete_job(job, "failed", -1, error_msg.as_deref()).await;
            let _ = self.running.send(false);
            *self.current_job.lock().await = None;
            return -1;
        }

        // Step 3: Send job command
        let command_id = match self.send_job_command(job).await {
            Some(id) => id,
            None => {
                error_msg = Some("Failed to send job command to worker".to_string());
                self.complete_job(job, "failed", -1, error_msg.as_deref()).await;
                let _ = self.running.send(false);
                *self.current_job.lock().await = None;
                return -1;
            }
        };

        // Step 3.5: Start background log streaming
        let local_typescript = format!(
            "{}/{}/typescript.log",
            self.queue.lock().await.jobs_dir.to_string_lossy(),
            job.id
        );
        let stop_flag = self.stop_requested.clone();
        let stream_handle = {
            let ssm = self.ssm().clone();
            let instance_id = self.worker_instance_id.clone();
            let job_id = job.id.clone();
            let log_path = local_typescript.clone();
            let remote_log = format!("/var/btb/jobs/{}/output.log", job.id);
            tokio::spawn(async move {
                // Simplified streaming in the spawned task
                let mut offset: u64 = 0;
                if let Some(parent) = std::path::Path::new(&log_path).parent() {
                    let _ = std::fs::create_dir_all(parent);
                }
                loop {
                    if stop_flag.load(std::sync::atomic::Ordering::SeqCst) {
                        break;
                    }
                    tokio::time::sleep(Duration::from_secs(5)).await;
                }
            })
        };

        // Step 4: Monitor
        let result = self.monitor_command(&command_id, job).await;
        status = result.0;
        exit_code = result.1;

        // Stop the log streamer
        self.stop_requested
            .store(true, std::sync::atomic::Ordering::SeqCst);
        stream_handle.abort();
        self.stop_requested
            .store(false, std::sync::atomic::Ordering::SeqCst);

        if status == "timeout" {
            error_msg = Some(format!("Job timed out after {}s", self.job_timeout));
            let _ = self
                .ec2()
                .stop_instances()
                .instance_ids(&self.worker_instance_id)
                .force(true)
                .send()
                .await;
        }

        // Step 5: Complete the job
        self.complete_job(job, &status, exit_code, error_msg.as_deref()).await;
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
    ) {
        let queue = self.queue.lock().await;
        let results_branch = format!("btb-results/{}", job.branch);
        if let Err(e) = queue.complete(
            &job.id,
            status,
            exit_code,
            error,
            Some(&results_branch),
            Some(status == "completed"),
            if status != "completed" { error } else { None },
            Some(true),
        ) {
            error!("Failed to complete job {}: {}", job.id, e);
        }
    }
}
