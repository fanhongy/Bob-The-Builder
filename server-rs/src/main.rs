//! BTB Service entry point.
//!
//! Wires together all components: config loader, job queue, result pusher,
//! job executor, TUI streamer manager, webhook receiver, and web dashboard.
//! Starts background tasks for queue polling, log cleanup, and credential
//! refresh, sets up TLS, and runs the axum application.

mod config;
mod dashboard;
mod ec2_executor;
mod executor;
mod models;
mod pusher;
mod queue;
mod streamer;
mod webhook;

use crate::config::{load_config, Config};
use crate::executor::JobExecutor;
use crate::models::Job;
use crate::pusher::ResultPusher;
use crate::queue::JobQueue;
use crate::streamer::TUIStreamerManager;
use axum::{
    routing::{get, post},
    Router,
};
use std::net::SocketAddr;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::process::Command;
use tracing::{error, info, warn};

/// Background task intervals
const QUEUE_POLL_INTERVAL: u64 = 5;
const LOG_CLEANUP_INTERVAL: u64 = 86400;
const CREDENTIAL_REFRESH_INTERVAL: u64 = 3600;

/// Shared application state accessible from all handlers.
pub struct AppState {
    pub config: Config,
    pub job_queue: Mutex<JobQueue>,
    pub executor: Arc<JobExecutor>,
    pub streamer_manager: Arc<TUIStreamerManager>,
}

/// Mark any jobs left in 'running' state as failed on startup.
fn recover_orphaned_jobs(queue: &JobQueue) -> u32 {
    let mut recovered = 0;
    for filepath in queue.sorted_queue_files() {
        let content = match std::fs::read_to_string(&filepath) {
            Ok(c) => c,
            Err(_) => continue,
        };
        let job: Job = match serde_json::from_str(&content) {
            Ok(j) => j,
            Err(_) => continue,
        };
        if job.status == "running" {
            warn!(
                "Found orphaned running job {} (spec={}) - marking as failed",
                job.id, job.spec_name
            );
            let results_branch = format!("btb-results/{}", job.branch);
            if queue
                .complete(
                    &job.id,
                    "failed",
                    -1,
                    Some("Coordinator restarted while job was in flight"),
                    Some(&results_branch),
                    Some(false),
                    Some("Coordinator restarted while job was in flight"),
                    Some(true),
                )
                .is_ok()
            {
                recovered += 1;
            }
        }
    }
    if recovered > 0 {
        info!("Recovered {} orphaned job(s)", recovered);
    }
    recovered
}

/// Periodically delete completed jobs older than the retention period.
async fn log_cleanup_task(config: Config) {
    let retention_days = config.log_retention_days;
    let logs_dir = PathBuf::from(&config.logs_dir);
    let completed_dir = PathBuf::from(&config.completed_dir);

    loop {
        tokio::time::sleep(Duration::from_secs(LOG_CLEANUP_INTERVAL)).await;

        info!("Running log cleanup - deleting jobs older than {} days", retention_days);

        let cutoff = chrono::Utc::now() - chrono::Duration::days(retention_days as i64);

        if !completed_dir.exists() {
            continue;
        }

        let entries = match std::fs::read_dir(&completed_dir) {
            Ok(e) => e,
            Err(_) => continue,
        };

        let mut deleted = 0;
        for entry in entries.flatten() {
            let path = entry.path();
            if path.extension().map_or(true, |ext| ext != "json") {
                continue;
            }
            let content = match std::fs::read_to_string(&path) {
                Ok(c) => c,
                Err(_) => continue,
            };
            let job: Job = match serde_json::from_str(&content) {
                Ok(j) => j,
                Err(_) => continue,
            };
            if let Some(completed_at) = &job.completed_at {
                if let Ok(completed_dt) = chrono::DateTime::parse_from_rfc3339(completed_at) {
                    if completed_dt < cutoff {
                        let _ = std::fs::remove_file(&path);
                        let job_logs = logs_dir.join(&job.id);
                        if job_logs.exists() {
                            let _ = std::fs::remove_dir_all(&job_logs);
                        }
                        deleted += 1;
                    }
                }
            }
        }

        if deleted > 0 {
            info!("Log cleanup: deleted {} expired job(s)", deleted);
        }
    }
}

/// Periodically refresh AWS SSO credentials.
async fn credential_refresh_task(config: Config) {
    let profile = config.aws_profile.clone();

    loop {
        tokio::time::sleep(Duration::from_secs(CREDENTIAL_REFRESH_INTERVAL)).await;

        let max_retries = 5;
        let mut success = false;

        for attempt in 0..max_retries {
            let output = Command::new("aws")
                .args(["sso", "login", "--profile", &profile])
                .output()
                .await;

            match output {
                Ok(out) if out.status.success() => {
                    info!("SSO credential refresh succeeded");
                    success = true;
                    break;
                }
                Ok(out) => {
                    let stderr = String::from_utf8_lossy(&out.stderr);
                    warn!(
                        "SSO credential refresh attempt {}/{} failed: {}",
                        attempt + 1,
                        max_retries,
                        stderr.trim()
                    );
                }
                Err(e) => {
                    warn!(
                        "SSO credential refresh attempt {}/{} error: {}",
                        attempt + 1,
                        max_retries,
                        e
                    );
                }
            }

            if attempt < max_retries - 1 {
                let backoff = 2u64.pow(attempt as u32);
                info!("Retrying credential refresh in {} seconds", backoff);
                tokio::time::sleep(Duration::from_secs(backoff)).await;
            }
        }

        if !success {
            error!("SSO credential refresh failed after {} attempts", max_retries);
        }
    }
}

/// Queue poller background task.
async fn queue_poller_task(state: Arc<AppState>) {
    loop {
        tokio::time::sleep(Duration::from_secs(QUEUE_POLL_INTERVAL)).await;

        if state.executor.is_running() {
            continue;
        }

        // Try to dequeue a job
        let job = {
            let queue = state.job_queue.lock().unwrap();
            match queue.dequeue() {
                Ok(Some(job)) => job,
                Ok(None) => continue,
                Err(e) => {
                    error!("Error dequeuing job: {}", e);
                    continue;
                }
            }
        };

        info!("Queue poller picked up job {}", job.id);

        // Create a TUI streamer for this job
        let typescript_path = format!("{}/{}/typescript.log", state.config.jobs_dir, job.id);
        let streamer = state
            .streamer_manager
            .get_or_create(&job.id, &typescript_path)
            .await;

        // Start the stream loop as a background task
        let streamer_clone = streamer.clone();
        let stream_task = tokio::spawn(async move {
            streamer_clone.stream_loop().await;
        });

        // Run the job
        let _exit_code = state.executor.run(&job).await;

        // Stop and remove the streamer
        state.streamer_manager.remove(&job.id).await;

        // Wait briefly for stream loop to finish
        let _ = tokio::time::timeout(Duration::from_secs(2), stream_task).await;
    }
}

#[tokio::main]
async fn main() {
    // Configure logging
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    // Load configuration
    let config_path = std::env::args()
        .nth(1)
        .unwrap_or_else(|| "/etc/btb-service/config.env".to_string());

    let config = match load_config(&config_path) {
        Ok(c) => c,
        Err(e) => {
            eprintln!("ERROR: {}", e);
            std::process::exit(1);
        }
    };

    info!("BTB Service starting with config from {}", config_path);

    // Initialize components
    let lock_file = PathBuf::from(&config.queue_dir)
        .parent()
        .unwrap_or(Path::new("/var/btb"))
        .join(".queue-lock");

    let queue = JobQueue::new(
        &config.queue_dir,
        &config.completed_dir,
        &config.jobs_dir,
        Some(lock_file.to_str().unwrap()),
    )
    .expect("Failed to create job queue");

    // Recover orphaned jobs
    recover_orphaned_jobs(&queue);

    let queue = Arc::new(Mutex::new(queue));
    let pusher = Arc::new(ResultPusher::new(Some(&config.btb_path)));

    // Create executor (local mode for now; EC2 mode initialized separately)
    let executor = Arc::new(JobExecutor::new(
        &config.btb_path,
        &config.jobs_dir,
        &config.logs_dir,
        pusher.clone(),
        Arc::new(tokio::sync::Mutex::new(
            JobQueue::new(
                &config.queue_dir,
                &config.completed_dir,
                &config.jobs_dir,
                Some(lock_file.to_str().unwrap()),
            )
            .expect("Failed to create executor queue"),
        )),
        config.job_timeout,
        Some(&config.github_token),
    ));

    let streamer_manager = Arc::new(TUIStreamerManager::new());

    let state = Arc::new(AppState {
        config: config.clone(),
        job_queue: Mutex::new(
            JobQueue::new(
                &config.queue_dir,
                &config.completed_dir,
                &config.jobs_dir,
                Some(lock_file.to_str().unwrap()),
            )
            .expect("Failed to create app state queue"),
        ),
        executor: executor.clone(),
        streamer_manager: streamer_manager.clone(),
    });

    // Build router
    let app = Router::new()
        .route("/", get(dashboard::handle_index))
        .route("/webhook", post(webhook::handle_webhook))
        .route("/api/jobs", get(dashboard::handle_jobs_list))
        .route("/api/jobs/{id}", get(dashboard::handle_job_detail))
        .route("/api/jobs/{id}/logs", get(dashboard::handle_job_logs_list))
        .route(
            "/api/jobs/{id}/logs/{*filename}",
            get(dashboard::handle_job_log_file),
        )
        .route("/api/jobs/{id}/retry", post(dashboard::handle_job_retry))
        .route("/ws/jobs/{id}/tui", get(dashboard::handle_tui_websocket))
        .with_state(state.clone());

    // Start background tasks
    let config_clone = config.clone();
    tokio::spawn(queue_poller_task(state.clone()));
    tokio::spawn(log_cleanup_task(config_clone.clone()));
    tokio::spawn(credential_refresh_task(config_clone));

    info!("Background tasks started");

    // Start the server
    let addr = SocketAddr::from(([0, 0, 0, 0], config.port));

    // Check for TLS
    let cert_path = Path::new(&config.tls_cert);
    let key_path = Path::new(&config.tls_key);

    if cert_path.exists() && key_path.exists() {
        info!("TLS configured with cert={}", config.tls_cert);

        let tls_config = axum_server::tls_rustls::RustlsConfig::from_pem_file(
            &config.tls_cert,
            &config.tls_key,
        )
        .await
        .expect("Failed to load TLS certificate");

        info!("BTB Service listening on https://{}", addr);
        axum_server::bind_rustls(addr, tls_config)
            .serve(app.into_make_service())
            .await
            .expect("Server failed");
    } else {
        warn!(
            "TLS certificate or key not found (cert={}, key={}) - running without TLS",
            config.tls_cert, config.tls_key
        );
        info!("BTB Service listening on http://{}", addr);
        let listener = tokio::net::TcpListener::bind(addr)
            .await
            .expect("Failed to bind");
        axum::serve(listener, app)
            .await
            .expect("Server failed");
    }
}
