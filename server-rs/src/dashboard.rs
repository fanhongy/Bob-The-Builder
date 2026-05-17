//! Web dashboard API routes for the BTB Service.
//!
//! Provides route handlers for the dashboard UI, job listing/detail APIs,
//! log file access, job retry, and WebSocket TUI streaming.

use crate::models::Job;
use crate::AppState;
use axum::{
    extract::{ws::WebSocket, Path, State, WebSocketUpgrade},
    http::StatusCode,
    response::{Html, IntoResponse, Response},
    Json,
};
use chrono::Utc;
use futures::StreamExt;
use serde_json::json;
use std::path::PathBuf;
use std::sync::Arc;
use tracing::{info, warn};
use uuid::Uuid;

/// Serve the single-page HTML dashboard.
pub async fn handle_index() -> impl IntoResponse {
    let index_path = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("static/index.html");
    match std::fs::read_to_string(&index_path) {
        Ok(content) => Html(content).into_response(),
        Err(_) => {
            // Fallback: try relative path
            match std::fs::read_to_string("static/index.html") {
                Ok(content) => Html(content).into_response(),
                Err(_) => (StatusCode::NOT_FOUND, "Dashboard not found").into_response(),
            }
        }
    }
}

/// Return a JSON list of running, pending, and completed jobs.
pub async fn handle_jobs_list(State(state): State<Arc<AppState>>) -> impl IntoResponse {
    let queue = state.job_queue.lock().unwrap();

    let running_job = queue.get_running();
    let pending_jobs = queue.get_pending();
    let completed_jobs = queue.get_completed(50);

    let result = json!({
        "running": running_job,
        "pending": pending_jobs,
        "completed": completed_jobs,
    });

    Json(result)
}

/// Return JSON details for a single job by ID.
pub async fn handle_job_detail(
    State(state): State<Arc<AppState>>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    let queue = state.job_queue.lock().unwrap();

    match queue.get_job(&id) {
        Some(job) => Json(json!(job)).into_response(),
        None => (StatusCode::NOT_FOUND, Json(json!({"error": "Job not found"}))).into_response(),
    }
}

/// List log files for a completed job.
pub async fn handle_job_logs_list(
    State(state): State<Arc<AppState>>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    let queue = state.job_queue.lock().unwrap();

    if queue.get_job(&id).is_none() {
        return (StatusCode::NOT_FOUND, Json(json!({"error": "Job not found"}))).into_response();
    }

    let job_logs_dir = PathBuf::from(&state.config.logs_dir).join(&id);
    if !job_logs_dir.exists() || !job_logs_dir.is_dir() {
        return Json(json!({"files": []})).into_response();
    }

    let mut files = Vec::new();
    if let Ok(entries) = walkdir(&job_logs_dir, &job_logs_dir) {
        files = entries;
    }
    files.sort();

    Json(json!({"files": files})).into_response()
}

/// Serve a specific log file for a job.
pub async fn handle_job_log_file(
    State(state): State<Arc<AppState>>,
    Path((id, filename)): Path<(String, String)>,
) -> impl IntoResponse {
    let queue = state.job_queue.lock().unwrap();

    if queue.get_job(&id).is_none() {
        return (StatusCode::NOT_FOUND, "Job not found".to_string()).into_response();
    }

    let job_logs_dir = PathBuf::from(&state.config.logs_dir).join(&id);
    let file_path = job_logs_dir.join(&filename);

    // Security: ensure the resolved path is within the job logs directory
    let resolved = match file_path.canonicalize() {
        Ok(p) => p,
        Err(_) => return (StatusCode::NOT_FOUND, "Log file not found".to_string()).into_response(),
    };

    let base = match job_logs_dir.canonicalize() {
        Ok(p) => p,
        Err(_) => return (StatusCode::NOT_FOUND, "Log file not found".to_string()).into_response(),
    };

    if !resolved.starts_with(&base) {
        return (StatusCode::NOT_FOUND, "Invalid file path".to_string()).into_response();
    }

    match std::fs::read_to_string(&resolved) {
        Ok(content) => content.into_response(),
        Err(_) => (StatusCode::NOT_FOUND, "Log file not found".to_string()).into_response(),
    }
}

/// Enqueue a new job as a retry of the specified job.
pub async fn handle_job_retry(
    State(state): State<Arc<AppState>>,
    Path(id): Path<String>,
) -> impl IntoResponse {
    let queue = state.job_queue.lock().unwrap();

    let original_job = match queue.get_job(&id) {
        Some(j) => j,
        None => {
            return (StatusCode::NOT_FOUND, Json(json!({"error": "Job not found"}))).into_response();
        }
    };

    // Only allow retry of terminal-state jobs
    let terminal_states = ["completed", "failed", "timeout"];
    if !terminal_states.contains(&original_job.status.as_str()) {
        return (
            StatusCode::CONFLICT,
            Json(json!({"error": format!("Job is in state '{}', not a terminal state", original_job.status)})),
        )
            .into_response();
    }

    // Create a new job with the same parameters
    let new_job = Job {
        id: Uuid::new_v4().to_string(),
        repo_url: original_job.repo_url.clone(),
        branch: original_job.branch.clone(),
        commit_sha: original_job.commit_sha.clone(),
        pusher: original_job.pusher.clone(),
        spec_name: original_job.spec_name.clone(),
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
        retry_of: Some(original_job.id.clone()),
        stopped_at: None,
        preserve_workdir: false,
    };

    match queue.enqueue(&new_job) {
        Ok(new_job_id) => {
            info!("Retried job {} as new job {}", id, new_job_id);
            (StatusCode::CREATED, Json(json!({"job_id": new_job_id}))).into_response()
        }
        Err(e) => (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(json!({"error": format!("Failed to enqueue: {}", e)})),
        )
            .into_response(),
    }
}

/// WebSocket endpoint for live TUI streaming.
pub async fn handle_tui_websocket(
    State(state): State<Arc<AppState>>,
    Path(id): Path<String>,
    ws: WebSocketUpgrade,
) -> Response {
    // Verify the job exists
    {
        let queue = state.job_queue.lock().unwrap();
        if queue.get_job(&id).is_none() {
            return (StatusCode::NOT_FOUND, "Job not found").into_response();
        }
    }

    ws.on_upgrade(move |socket| handle_ws_connection(socket, state, id))
}

async fn handle_ws_connection(socket: WebSocket, state: Arc<AppState>, job_id: String) {
    let (sender, mut receiver) = socket.split();

    // Get or create a streamer for this job
    let typescript_path = format!("{}/{}/typescript.log", state.config.jobs_dir, job_id);
    let streamer = state
        .streamer_manager
        .get_or_create(&job_id, &typescript_path)
        .await;

    // Add the client
    streamer.add_client(sender).await;

    // Read messages in a loop until the client disconnects
    while let Some(msg) = receiver.next().await {
        match msg {
            Ok(_) => {} // Ignore all messages from the client
            Err(_) => break,
        }
    }

    // Client disconnected — removal happens automatically when broadcast fails
}

/// Recursively walk a directory and return relative file paths.
fn walkdir(base: &PathBuf, dir: &PathBuf) -> std::io::Result<Vec<String>> {
    let mut files = Vec::new();
    if dir.is_dir() {
        for entry in std::fs::read_dir(dir)? {
            let entry = entry?;
            let path = entry.path();
            if path.is_dir() {
                files.extend(walkdir(base, &path)?);
            } else if path.is_file() {
                if let Ok(relative) = path.strip_prefix(base) {
                    files.push(relative.to_string_lossy().to_string());
                }
            }
        }
    }
    Ok(files)
}
