//! Webhook receiver for GitHub push events.
//!
//! Validates webhook signatures, extracts push payload data, checks for .btb files
//! via the GitHub Contents API, and enqueues jobs when appropriate.

use crate::models::Job;
use crate::queue::JobQueue;
use axum::{
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
    response::IntoResponse,
};
use chrono::Utc;
use hmac::{Hmac, Mac};
use reqwest::Client;
use sha2::Sha256;
use std::sync::Arc;
use tracing::{debug, error, info, warn};
use uuid::Uuid;

use crate::AppState;

type HmacSha256 = Hmac<Sha256>;

/// Verify the GitHub webhook HMAC-SHA256 signature.
fn verify_signature(payload_body: &[u8], secret: &str, signature_header: &str) -> bool {
    if signature_header.is_empty() {
        return false;
    }

    let Some(expected_sig) = signature_header.strip_prefix("sha256=") else {
        return false;
    };

    let mut mac =
        HmacSha256::new_from_slice(secret.as_bytes()).expect("HMAC can take key of any size");
    mac.update(payload_body);
    let computed = hex::encode(mac.finalize().into_bytes());

    // Constant-time comparison
    subtle::ConstantTimeEq::ct_eq(computed.as_bytes(), expected_sig.as_bytes()).into()
}

/// Parse a .btb file's key-value content into a HashMap.
fn parse_btb_file(content: &str) -> std::collections::HashMap<String, String> {
    let mut result = std::collections::HashMap::new();
    for line in content.lines() {
        let stripped = line.trim();
        if stripped.is_empty() || stripped.starts_with('#') {
            continue;
        }
        let Some((key, value)) = stripped.split_once('=') else {
            continue;
        };
        let key = key.trim().to_string();
        let mut value = value.trim().to_string();
        // Strip optional quotes
        if value.len() >= 2 {
            let first = value.chars().next().unwrap();
            let last = value.chars().last().unwrap();
            if first == last && (first == '"' || first == '\'') {
                value = value[1..value.len() - 1].to_string();
            }
        }
        if !key.is_empty() {
            result.insert(key, value);
        }
    }
    result
}

/// Result of checking a .btb file.
struct BtbFileResult {
    spec_name: Option<String>,
    #[allow(dead_code)]
    status: Option<String>,
    skip_reason: Option<String>,
}

/// Check for a .btb file at the repository root and return its spec name.
async fn check_btb_file(
    client: &Client,
    owner: &str,
    repo: &str,
    sha: &str,
    github_token: &str,
) -> Result<BtbFileResult, String> {
    let url = format!(
        "https://api.github.com/repos/{}/{}/contents/?ref={}",
        owner, repo, sha
    );

    let resp = client
        .get(&url)
        .header("Authorization", format!("token {}", github_token))
        .header("Accept", "application/vnd.github.v3+json")
        .header("User-Agent", "btb-server")
        .send()
        .await
        .map_err(|e| format!("GitHub API request failed: {}", e))?;

    if !resp.status().is_success() {
        let status = resp.status();
        // Do NOT log the response body — it may echo back auth headers or tokens
        return Err(format!(
            "GitHub API returned HTTP {} when listing repo contents",
            status
        ));
    }

    let contents: Vec<serde_json::Value> = resp
        .json()
        .await
        .map_err(|e| format!("Failed to parse GitHub API response: {}", e))?;

    // Find a .btb file
    let btb_file = contents.iter().find(|item| {
        item.get("type").and_then(|t| t.as_str()) == Some("file")
            && item
                .get("name")
                .and_then(|n| n.as_str())
                .map_or(false, |n| n.ends_with(".btb"))
    });

    let btb_file = match btb_file {
        Some(f) => f,
        None => {
            return Ok(BtbFileResult {
                spec_name: None,
                status: None,
                skip_reason: Some("no .btb file".to_string()),
            });
        }
    };

    let file_name = btb_file["name"].as_str().unwrap_or("");

    // Fetch the .btb file contents
    let file_url = format!(
        "https://api.github.com/repos/{}/{}/contents/{}?ref={}",
        owner, repo, file_name, sha
    );

    let file_resp = client
        .get(&file_url)
        .header("Authorization", format!("token {}", github_token))
        .header("Accept", "application/vnd.github.v3+json")
        .header("User-Agent", "btb-server")
        .send()
        .await
        .map_err(|e| format!("GitHub API request failed when fetching .btb file: {}", e))?;

    if file_resp.status() != 200 {
        return Ok(BtbFileResult {
            spec_name: None,
            status: None,
            skip_reason: Some("failed to fetch .btb file".to_string()),
        });
    }

    let file_data: serde_json::Value = file_resp
        .json()
        .await
        .map_err(|e| format!("Failed to parse .btb file response: {}", e))?;

    // Decode base64 content
    let content_b64 = file_data
        .get("content")
        .and_then(|c| c.as_str())
        .unwrap_or("");

    // GitHub returns base64 with newlines
    let content_b64_clean: String = content_b64.chars().filter(|c| !c.is_whitespace()).collect();

    let content_bytes = base64::Engine::decode(
        &base64::engine::general_purpose::STANDARD,
        &content_b64_clean,
    )
    .map_err(|_| "Failed to decode .btb file contents".to_string())?;

    let content = String::from_utf8_lossy(&content_bytes).to_string();

    // Parse the .btb file
    let parsed = parse_btb_file(&content);
    let spec_name = parsed.get("spec").map(|s| s.trim().to_string());
    let status = parsed.get("status").map(|s| s.trim().to_string());

    if spec_name.as_ref().map_or(true, |s| s.is_empty()) {
        return Ok(BtbFileResult {
            spec_name: None,
            status: None,
            skip_reason: Some("no spec key in .btb file".to_string()),
        });
    }

    // Check if spec is already completed
    if status.as_deref() == Some("completed") {
        let sn = spec_name.clone().unwrap_or_default();
        return Ok(BtbFileResult {
            spec_name,
            status,
            skip_reason: Some(format!("spec '{}' already completed", sn)),
        });
    }

    Ok(BtbFileResult {
        spec_name,
        status,
        skip_reason: None,
    })
}

/// Check if any .btb file was added or modified in the pushed commits.
fn btb_file_changed(payload: &serde_json::Value) -> bool {
    if let Some(commits) = payload.get("commits").and_then(|c| c.as_array()) {
        for commit in commits {
            let added = commit
                .get("added")
                .and_then(|a| a.as_array())
                .into_iter()
                .flatten();
            let modified = commit
                .get("modified")
                .and_then(|m| m.as_array())
                .into_iter()
                .flatten();

            for path_val in added.chain(modified) {
                if let Some(path) = path_val.as_str() {
                    // Root-level .btb file: no "/" in path, ends with .btb
                    if !path.contains('/') && path.ends_with(".btb") {
                        return true;
                    }
                }
            }
        }
    }
    false
}

/// Handle incoming GitHub webhook events.
pub async fn handle_webhook(
    State(state): State<Arc<AppState>>,
    headers: HeaderMap,
    body: Bytes,
) -> impl IntoResponse {
    // Validate signature
    let signature = headers
        .get("x-hub-signature-256")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");

    if !verify_signature(&body, &state.config.webhook_secret, signature) {
        warn!("Invalid webhook signature");
        return (StatusCode::FORBIDDEN, "Invalid signature".to_string());
    }

    // Check event type
    let event_type = headers
        .get("x-github-event")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");

    // Handle ping event
    if event_type == "ping" {
        info!("Received ping event - webhook is configured correctly");
        return (StatusCode::OK, "Pong".to_string());
    }

    // Skip non-push events
    if event_type != "push" {
        debug!("Skipping non-push event: {}", event_type);
        return (
            StatusCode::OK,
            format!("Skipped: {} event", event_type),
        );
    }

    // Parse JSON payload
    let payload: serde_json::Value = match serde_json::from_slice(&body) {
        Ok(v) => v,
        Err(_) => {
            error!("Failed to parse webhook payload as JSON");
            return (
                StatusCode::BAD_REQUEST,
                "Invalid JSON payload".to_string(),
            );
        }
    };

    // Skip branch/tag deletion events
    if payload.get("after").and_then(|v| v.as_str())
        == Some("0000000000000000000000000000000000000000")
    {
        debug!("Skipping branch/tag deletion event");
        return (StatusCode::OK, "Skipped: deletion event".to_string());
    }

    // Extract required fields
    let repo = match payload.get("repository") {
        Some(r) => r,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                "Missing required field: repository".to_string(),
            );
        }
    };

    let repo_url = repo
        .get("clone_url")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let full_name = repo
        .get("full_name")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let ref_field = payload
        .get("ref")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let commit_sha = payload
        .get("after")
        .and_then(|v| v.as_str())
        .unwrap_or("")
        .to_string();
    let pusher_name = payload
        .get("pusher")
        .and_then(|p| p.get("name"))
        .and_then(|n| n.as_str())
        .unwrap_or("")
        .to_string();

    // Extract branch name
    let branch = ref_field
        .strip_prefix("refs/heads/")
        .unwrap_or(&ref_field)
        .to_string();

    // Loop prevention: skip btb-results/ branches
    if branch.starts_with("btb-results/") {
        debug!("Skipping push to results branch: {}", branch);
        return (StatusCode::OK, "Skipped: results branch".to_string());
    }

    // Only trigger when the .btb file was explicitly changed
    if !btb_file_changed(&payload) {
        info!(
            "Skipping push to {}/{} - .btb file not modified in commits",
            full_name, branch
        );
        return (
            StatusCode::OK,
            "Skipped: .btb file not changed".to_string(),
        );
    }

    // Check for .btb file via GitHub API
    let (owner, repo_name) = match full_name.split_once('/') {
        Some(parts) => parts,
        None => {
            return (
                StatusCode::BAD_REQUEST,
                "Invalid repository full_name".to_string(),
            );
        }
    };

    let client = Client::new();
    let btb_result = match check_btb_file(
        &client,
        owner,
        repo_name,
        &commit_sha,
        &state.config.github_token,
    )
    .await
    {
        Ok(r) => r,
        Err(e) => {
            // Log only the sanitized error — never log the token or full URLs with credentials
            error!(
                "GitHub API error checking .btb file for {}/{}: {}",
                owner, repo_name, e
            );
            return (
                StatusCode::INTERNAL_SERVER_ERROR,
                "GitHub API error".to_string(),
            );
        }
    };

    if let Some(reason) = btb_result.skip_reason {
        debug!("Skipping job for {}: {}", full_name, reason);
        return (StatusCode::OK, format!("Skipped: {}", reason));
    }

    let spec_name = match btb_result.spec_name {
        Some(s) => s,
        None => {
            return (
                StatusCode::OK,
                "Skipped: no spec name".to_string(),
            );
        }
    };

    // Create and enqueue job
    let job = Job {
        id: Uuid::new_v4().to_string(),
        repo_url,
        branch: branch.clone(),
        commit_sha,
        pusher: pusher_name,
        spec_name: spec_name.clone(),
        status: "pending".to_string(),
        submitted_at: Utc::now().to_rfc3339(),
        started_at: None,
        completed_at: None,
        exit_code: None,
        error: None,
        results_branch: None,
        push_success: None,
        push_error: None,
        cleanup_success: None,
        retry_of: None,
        stopped_at: None,
        preserve_workdir: false,
    };

    let queue = state.job_queue.lock().unwrap();
    match queue.enqueue(&job) {
        Ok(job_id) => {
            info!(
                "Enqueued job {} for {} branch={} spec={}",
                job_id, full_name, branch, spec_name
            );
            (StatusCode::OK, format!("Job enqueued: {}", job_id))
        }
        Err(e) => {
            error!("Failed to enqueue job: {}", e);
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                format!("Failed to enqueue job: {}", e),
            )
        }
    }
}
