//! Result pusher for the BTB Service.
//!
//! Pushes btb results back to the original repository in two stages:
//!
//! 1. **Results branch** (`btb-results/{branch}`): Force-pushed as a receipt/debug artifact.
//! 2. **Source branch** (`{branch}`): Squash-rebased on top of the latest `origin/{branch}`.
//!
//! Before pushing, the `.btb` file is updated with `status`, `last_run`, and `job_id` keys.

use crate::models::{Job, PushResult};
use chrono::Utc;
use glob::glob;
use std::path::Path;
use std::process::Stdio;
use tokio::process::Command;
use tracing::{error, info, warn};

/// Update the .btb file in the repo with job status metadata.
pub fn update_btb_file(repo_dir: &str, status: &str, job_id: &str, timestamp: Option<&str>) -> bool {
    let timestamp = timestamp
        .map(|s| s.to_string())
        .unwrap_or_else(|| Utc::now().to_rfc3339());

    // Find the .btb file
    let pattern = format!("{}/*.btb", repo_dir);
    let mut btb_files: Vec<String> = glob(&pattern)
        .into_iter()
        .flatten()
        .flatten()
        .map(|p| p.to_string_lossy().to_string())
        .collect();

    // Also check for hidden .btb file
    let hidden_btb = format!("{}/.btb", repo_dir);
    if Path::new(&hidden_btb).is_file() {
        btb_files.insert(0, hidden_btb);
    }

    if btb_files.is_empty() {
        warn!("No .btb file found in {} - skipping status update", repo_dir);
        return false;
    }

    let btb_path = &btb_files[0];

    let content = match std::fs::read_to_string(btb_path) {
        Ok(c) => c,
        Err(e) => {
            warn!("Failed to read .btb file {}: {}", btb_path, e);
            return false;
        }
    };

    let updates = [
        ("status", status.to_string()),
        ("last_run", timestamp),
        ("job_id", job_id.to_string()),
    ];

    let mut seen_keys = std::collections::HashSet::new();
    let mut new_lines: Vec<String> = Vec::new();

    for line in content.lines() {
        let stripped = line.trim();
        if stripped.is_empty() || stripped.starts_with('#') {
            new_lines.push(line.to_string());
            continue;
        }
        if !stripped.contains('=') {
            new_lines.push(line.to_string());
            continue;
        }

        let key = stripped.split('=').next().unwrap_or("").trim();

        if let Some((_, value)) = updates.iter().find(|(k, _)| *k == key) {
            new_lines.push(format!("{}={}", key, value));
            seen_keys.insert(key.to_string());
        } else {
            new_lines.push(line.to_string());
        }
    }

    // Append any keys that weren't already in the file
    for (key, value) in &updates {
        if !seen_keys.contains(*key) {
            new_lines.push(format!("{}={}", key, value));
        }
    }

    match std::fs::write(btb_path, new_lines.join("\n") + "\n") {
        Ok(_) => {
            info!("Updated .btb file with status={} for job {}", status, job_id);
            true
        }
        Err(e) => {
            warn!("Failed to write .btb file {}: {}", btb_path, e);
            false
        }
    }
}

/// Run a git command and return (exit_code, stdout, stderr).
async fn run_git(args: &[&str], cwd: &str) -> (i32, String, String) {
    let output = Command::new("git")
        .args(args)
        .current_dir(cwd)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .output()
        .await;

    match output {
        Ok(out) => {
            let stdout = String::from_utf8_lossy(&out.stdout).trim().to_string();
            let stderr = String::from_utf8_lossy(&out.stderr).trim().to_string();
            let code = out.status.code().unwrap_or(-1);
            (code, stdout, stderr)
        }
        Err(e) => (-1, String::new(), format!("Failed to execute git: {}", e)),
    }
}

/// Pushes btb results back to the original repository.
pub struct ResultPusher {
    btb_path: Option<String>,
}

impl ResultPusher {
    pub fn new(btb_path: Option<&str>) -> Self {
        Self {
            btb_path: btb_path.map(|s| s.to_string()),
        }
    }

    /// Compute the results branch name for a given source branch.
    pub fn compute_results_branch(branch: &str) -> String {
        format!("btb-results/{}", branch)
    }

    /// Push btb results to the results branch in the original repo.
    pub async fn push_results(
        &self,
        job: &Job,
        repo_dir: &str,
        status: &str,
        job_id: &str,
        timestamp: Option<&str>,
    ) -> PushResult {
        let results_branch = Self::compute_results_branch(&job.branch);

        // Step 0: Update .btb file with job status metadata
        if !status.is_empty() && !job_id.is_empty() {
            update_btb_file(repo_dir, status, job_id, timestamp);
        }

        // Step 1: Stage all changes
        let (rc, _, stderr) = run_git(&["add", "-A"], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("git add -A failed (rc={}): {}", rc, stderr);
            error!("Push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: results_branch,
                error: Some(error_msg),
            };
        }

        // Step 2: Check if there are staged changes
        let (rc, _, _) = run_git(&["diff", "--cached", "--quiet"], repo_dir).await;
        if rc == 0 {
            info!("No changes to commit for job {}, pushing current HEAD", job.id);
        } else {
            // Step 3: Commit the changes
            let commit_msg = format!("btb results for {} [job: {}]", job.spec_name, job.id);
            let (rc, _, stderr) = run_git(&["commit", "-m", &commit_msg], repo_dir).await;
            if rc != 0 {
                let error_msg = format!("git commit failed (rc={}): {}", rc, stderr);
                error!("Push failed for job {}: {}", job.id, error_msg);
                return PushResult {
                    success: false,
                    branch: results_branch,
                    error: Some(error_msg),
                };
            }
            info!("Committed btb results for job {}", job.id);
        }

        // Step 4: Force-push to the results branch
        let push_ref = format!("HEAD:refs/heads/{}", results_branch);
        let (rc, _, stderr) = run_git(&["push", "--force", "origin", &push_ref], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("git push failed (rc={}): {}", rc, stderr);
            error!("Push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: results_branch,
                error: Some(error_msg),
            };
        }

        info!(
            "Successfully pushed results for job {} to {}",
            job.id, results_branch
        );
        PushResult {
            success: true,
            branch: results_branch,
            error: None,
        }
    }

    /// Squash-rebase btb work onto the source branch and push.
    pub async fn push_to_source_branch(&self, job: &Job, repo_dir: &str) -> PushResult {
        let source_branch = &job.branch;

        // Step 1: Fetch latest origin/{branch}
        info!(
            "Fetching latest origin/{} for squash-rebase (job {})",
            source_branch, job.id
        );
        let (rc, _, err) = run_git(&["fetch", "origin", source_branch], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("git fetch origin {} failed: {}", source_branch, err);
            error!("Source push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: source_branch.clone(),
                error: Some(error_msg),
            };
        }

        // Step 2: Record the merge base
        let origin_branch = format!("origin/{}", source_branch);
        let (rc, merge_base, _) = run_git(&["merge-base", "HEAD", &origin_branch], repo_dir).await;
        let merge_base = if rc != 0 {
            warn!(
                "merge-base failed for job {}, using clone commit {}",
                job.id, job.commit_sha
            );
            job.commit_sha.clone()
        } else {
            merge_base
        };

        // Step 3: Create a squash branch
        let squash_branch = format!("btb-squash-{}", &job.id[..8.min(job.id.len())]);
        let (rc, _, err) = run_git(&["checkout", "-b", &squash_branch], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("Failed to create squash branch: {}", err);
            error!("Source push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: source_branch.clone(),
                error: Some(error_msg),
            };
        }

        // Step 4: Soft-reset to merge base
        let (rc, _, err) = run_git(&["reset", "--soft", &merge_base], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("git reset --soft failed: {}", err);
            error!("Source push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: source_branch.clone(),
                error: Some(error_msg),
            };
        }

        // Step 5: Commit the squashed changes
        let short_id = &job.id[..8.min(job.id.len())];
        let commit_msg = format!("btb: {} [job: {}]", job.spec_name, short_id);
        let (rc, _, err) = run_git(&["commit", "-m", &commit_msg, "--allow-empty"], repo_dir).await;
        if rc != 0 {
            let error_msg = format!("Squash commit failed: {}", err);
            error!("Source push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: source_branch.clone(),
                error: Some(error_msg),
            };
        }

        info!("Squashed btb work into single commit for job {}", job.id);

        // Step 6: Rebase onto origin/{branch}
        let (rc, _, err) = run_git(&["rebase", &origin_branch], repo_dir).await;
        if rc != 0 {
            warn!(
                "Rebase conflict for job {} - invoking resolver agent",
                job.id
            );
            let resolved = self.resolve_rebase_conflicts(job, repo_dir).await;
            if !resolved {
                let _ = run_git(&["rebase", "--abort"], repo_dir).await;
                let error_msg = "Rebase conflicts could not be resolved".to_string();
                error!("Source push failed for job {}: {}", job.id, error_msg);
                return PushResult {
                    success: false,
                    branch: source_branch.clone(),
                    error: Some(error_msg),
                };
            }
        }

        info!("Rebase successful for job {}", job.id);

        // Step 7: Run verifier agent
        let verified = self.verify_build(job, repo_dir).await;
        if !verified {
            let error_msg = "Post-rebase build verification failed".to_string();
            error!("Source push failed for job {}: {}", job.id, error_msg);
            return PushResult {
                success: false,
                branch: source_branch.clone(),
                error: Some(error_msg),
            };
        }

        info!("Build verified for job {}", job.id);

        // Step 8: Push to origin/{branch}
        let push_ref = format!("HEAD:refs/heads/{}", source_branch);
        let (rc, _, err) = run_git(&["push", "origin", &push_ref], repo_dir).await;
        if rc != 0 {
            warn!(
                "Push to {} failed for job {}, retrying with fresh rebase: {}",
                source_branch, job.id, err
            );
            return self.retry_push(job, repo_dir, source_branch).await;
        }

        info!(
            "Successfully pushed squashed results for job {} to {}",
            job.id, source_branch
        );
        PushResult {
            success: true,
            branch: source_branch.clone(),
            error: None,
        }
    }

    /// Run a kiro-cli agent and return (exit_code, output).
    async fn run_agent(
        &self,
        agent: &str,
        prompt: &str,
        cwd: &str,
        timeout_secs: u64,
    ) -> (i32, String) {
        let btb_path = match &self.btb_path {
            Some(p) => p,
            None => return (-1, "btb_path not configured".to_string()),
        };

        // Ensure agent files are available
        let agent_src = format!("{}/.kiro/agents", btb_path);
        let agent_dst = format!("{}/.kiro/agents", cwd);
        if Path::new(&agent_src).is_dir() {
            let _ = std::fs::create_dir_all(&agent_dst);
            if let Ok(entries) = std::fs::read_dir(&agent_src) {
                for entry in entries.flatten() {
                    let src_file = entry.path();
                    let dst_file =
                        Path::new(&agent_dst).join(entry.file_name());
                    if src_file.is_file() && !dst_file.exists() {
                        let _ = std::fs::copy(&src_file, &dst_file);
                    }
                }
            }
        }

        let output = tokio::time::timeout(
            std::time::Duration::from_secs(timeout_secs),
            Command::new("kiro-cli")
                .args([
                    "chat",
                    "--no-interactive",
                    "--agent",
                    agent,
                    "--trust-all-tools",
                    prompt,
                ])
                .current_dir(cwd)
                .stdout(Stdio::piped())
                .stderr(Stdio::piped())
                .output(),
        )
        .await;

        match output {
            Ok(Ok(out)) => {
                let stdout = String::from_utf8_lossy(&out.stdout).to_string();
                let code = out.status.code().unwrap_or(-1);
                (code, stdout)
            }
            Ok(Err(e)) => (-1, format!("Failed to run agent: {}", e)),
            Err(_) => (-1, format!("Agent {} timed out after {}s", agent, timeout_secs)),
        }
    }

    /// Invoke the resolver agent to handle rebase conflicts.
    async fn resolve_rebase_conflicts(&self, job: &Job, repo_dir: &str) -> bool {
        let (_, conflicted, _) =
            run_git(&["diff", "--name-only", "--diff-filter=U"], repo_dir).await;
        if conflicted.trim().is_empty() {
            return false;
        }

        let prompt = format!(
            "REBASE CONFLICT RESOLUTION NEEDED\n\n\
            CONTEXT:\n- Job: {}\n- Spec: {}\n- Source branch: {}\n\n\
            CONFLICTED FILES:\n{}\n\n\
            Resolve each conflict, run 'git add <file>' on each, then 'git rebase --continue'.\n\
            Output 'CONFLICTS_RESOLVED' when done.",
            job.id, job.spec_name, job.branch, conflicted
        );

        let (_, output) = self.run_agent("resolver", &prompt, repo_dir, 300).await;
        if output.contains("CONFLICTS_RESOLVED") {
            info!("Resolver agent resolved rebase conflicts for job {}", job.id);
            return true;
        }
        error!("Resolver agent failed to resolve conflicts for job {}", job.id);
        false
    }

    /// Invoke the verifier agent to confirm the build passes.
    async fn verify_build(&self, job: &Job, repo_dir: &str) -> bool {
        let prompt = format!(
            "Verify that this repository builds and tests pass.\n\
            Post-rebase verification for btb job {}.\nSpec: {}\nBranch: {}\n\
            Report BUILD_VERIFIED, BUILD_FAILED, or BUILD_UNKNOWN.",
            job.id, job.spec_name, job.branch
        );

        let (_, output) = self.run_agent("verifier", &prompt, repo_dir, 600).await;
        if output.contains("BUILD_VERIFIED") {
            info!("Build verified for job {}", job.id);
            return true;
        }
        if output.contains("BUILD_UNKNOWN") {
            warn!(
                "Verifier could not determine build commands for job {} - allowing push",
                job.id
            );
            return true;
        }
        error!("Build verification failed for job {}", job.id);
        false
    }

    /// Retry the push after a fresh fetch + rebase.
    async fn retry_push(&self, job: &Job, repo_dir: &str, source_branch: &str) -> PushResult {
        let (rc, _, err) = run_git(&["fetch", "origin", source_branch], repo_dir).await;
        if rc != 0 {
            return PushResult {
                success: false,
                branch: source_branch.to_string(),
                error: Some(format!("Retry fetch failed: {}", err)),
            };
        }

        let origin_branch = format!("origin/{}", source_branch);
        let (rc, _, _) = run_git(&["rebase", &origin_branch], repo_dir).await;
        if rc != 0 {
            let resolved = self.resolve_rebase_conflicts(job, repo_dir).await;
            if !resolved {
                let _ = run_git(&["rebase", "--abort"], repo_dir).await;
                return PushResult {
                    success: false,
                    branch: source_branch.to_string(),
                    error: Some("Retry rebase conflicts unresolvable".to_string()),
                };
            }
        }

        let push_ref = format!("HEAD:refs/heads/{}", source_branch);
        let (rc, _, err) = run_git(&["push", "origin", &push_ref], repo_dir).await;
        if rc != 0 {
            return PushResult {
                success: false,
                branch: source_branch.to_string(),
                error: Some(format!("Retry push failed: {}", err)),
            };
        }

        info!("Retry push succeeded for job {} to {}", job.id, source_branch);
        PushResult {
            success: true,
            branch: source_branch.to_string(),
            error: None,
        }
    }
}
