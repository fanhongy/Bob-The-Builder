//! TUI Streamer for the BTB Service.
//!
//! Streams btb's terminal output (captured by the `script` command into a
//! typescript log file) to browser clients over WebSocket connections.
//!
//! The streamer tails the typescript file using tokio, accumulates a full
//! buffer for late-joining clients, and broadcasts new data chunks to all
//! connected WebSocket clients as binary messages (raw bytes with ANSI codes).

use axum::extract::ws::{Message, WebSocket};
use futures::stream::SplitSink;
use futures::SinkExt;
use std::collections::HashMap;
use std::path::Path;
use std::sync::Arc;
use tokio::fs::File;
use tokio::io::{AsyncReadExt, AsyncSeekExt, SeekFrom};
use tokio::sync::{Mutex, RwLock};
use tracing::{debug, error, info, warn};

/// How often (in milliseconds) the stream loop checks for new data.
const POLL_INTERVAL_MS: u64 = 200;

/// A connected WebSocket client sender.
type WsSender = SplitSink<WebSocket, Message>;

/// Streams a single job's typescript log to WebSocket clients.
pub struct TUIStreamer {
    pub job_id: String,
    typescript_path: String,
    clients: Arc<Mutex<Vec<Arc<Mutex<WsSender>>>>>,
    buffer: Arc<RwLock<Vec<u8>>>,
    stopped: Arc<tokio::sync::watch::Sender<bool>>,
    stopped_rx: tokio::sync::watch::Receiver<bool>,
}

impl TUIStreamer {
    /// Create a new TUI streamer for a job.
    pub fn new(job_id: &str, typescript_path: &str) -> Self {
        let (stopped_tx, stopped_rx) = tokio::sync::watch::channel(false);
        Self {
            job_id: job_id.to_string(),
            typescript_path: typescript_path.to_string(),
            clients: Arc::new(Mutex::new(Vec::new())),
            buffer: Arc::new(RwLock::new(Vec::new())),
            stopped: Arc::new(stopped_tx),
            stopped_rx,
        }
    }

    /// Return the number of currently connected clients.
    pub async fn client_count(&self) -> usize {
        self.clients.lock().await.len()
    }

    /// Add a WebSocket client and send the current buffer for catchup.
    pub async fn add_client(&self, sender: WsSender) {
        let sender = Arc::new(Mutex::new(sender));

        // Send current buffer for catchup BEFORE adding to clients set
        let buffer = self.buffer.read().await;
        if !buffer.is_empty() {
            let mut s = sender.lock().await;
            if let Err(e) = s.send(Message::Binary(buffer.clone())).await {
                warn!(
                    "Failed to send catchup to client for job {}: {}",
                    self.job_id, e
                );
                return;
            }
        }
        drop(buffer);

        // Now add to clients - they'll only receive future broadcasts
        self.clients.lock().await.push(sender);
    }

    /// Remove a WebSocket client from the set.
    pub async fn remove_client_by_index(&self, _index: usize) {
        // Clients that fail to receive are cleaned up during broadcast
    }

    /// Main loop that tails the typescript file and broadcasts data.
    pub async fn stream_loop(&self) {
        let path = Path::new(&self.typescript_path);
        let mut offset: u64 = 0;

        // Wait for the file to appear (btb may not have started yet)
        let mut rx = self.stopped_rx.clone();
        loop {
            if *rx.borrow() {
                return;
            }
            if path.exists() {
                break;
            }
            tokio::select! {
                _ = tokio::time::sleep(tokio::time::Duration::from_millis(POLL_INTERVAL_MS)) => {}
                _ = rx.changed() => {
                    if *rx.borrow() {
                        return;
                    }
                }
            }
        }

        // Open and tail the file
        let file = match File::open(path).await {
            Ok(f) => f,
            Err(e) => {
                error!(
                    "Failed to open typescript file for job {}: {}",
                    self.job_id, e
                );
                return;
            }
        };
        let mut file = file;

        loop {
            if *rx.borrow() {
                break;
            }

            // Seek and read new data
            if let Err(e) = file.seek(SeekFrom::Start(offset)).await {
                error!("Seek error for job {}: {}", self.job_id, e);
                break;
            }

            let mut chunk = Vec::new();
            match file.read_to_end(&mut chunk).await {
                Ok(n) if n > 0 => {
                    offset += n as u64;
                    // Append to buffer
                    self.buffer.write().await.extend_from_slice(&chunk);
                    // Broadcast to clients
                    self.broadcast(&chunk).await;
                }
                Ok(_) => {} // No new data
                Err(e) => {
                    error!("Read error for job {}: {}", self.job_id, e);
                    break;
                }
            }

            tokio::select! {
                _ = tokio::time::sleep(tokio::time::Duration::from_millis(POLL_INTERVAL_MS)) => {}
                _ = rx.changed() => {
                    if *rx.borrow() {
                        break;
                    }
                }
            }
        }

        // Final read — pick up any data written between last poll and stop
        if path.exists() {
            if let Ok(mut f) = File::open(path).await {
                if f.seek(SeekFrom::Start(offset)).await.is_ok() {
                    let mut chunk = Vec::new();
                    if f.read_to_end(&mut chunk).await.is_ok() && !chunk.is_empty() {
                        self.buffer.write().await.extend_from_slice(&chunk);
                        self.broadcast(&chunk).await;
                    }
                }
            }
        }
    }

    /// Signal the stream loop to stop.
    pub fn stop(&self) {
        let _ = self.stopped.send(true);
    }

    /// Check if the streamer has been stopped.
    pub fn is_stopped(&self) -> bool {
        *self.stopped_rx.borrow()
    }

    /// Send data to all connected clients.
    async fn broadcast(&self, data: &[u8]) {
        let mut clients = self.clients.lock().await;
        if clients.is_empty() {
            return;
        }

        let mut disconnected = Vec::new();

        for (i, client) in clients.iter().enumerate() {
            let mut sender = client.lock().await;
            if sender.send(Message::Binary(data.to_vec())).await.is_err() {
                disconnected.push(i);
            }
        }

        // Remove disconnected clients in reverse order to maintain indices
        for &i in disconnected.iter().rev() {
            clients.swap_remove(i);
        }
    }
}

/// Manages multiple TUI streamers, one per running job.
pub struct TUIStreamerManager {
    streamers: RwLock<HashMap<String, Arc<TUIStreamer>>>,
}

impl TUIStreamerManager {
    /// Create a new streamer manager.
    pub fn new() -> Self {
        Self {
            streamers: RwLock::new(HashMap::new()),
        }
    }

    /// Return an existing streamer for the job, or create a new one.
    pub async fn get_or_create(&self, job_id: &str, typescript_path: &str) -> Arc<TUIStreamer> {
        let streamers = self.streamers.read().await;
        if let Some(streamer) = streamers.get(job_id) {
            return streamer.clone();
        }
        drop(streamers);

        let mut streamers = self.streamers.write().await;
        // Double-check after acquiring write lock
        if let Some(streamer) = streamers.get(job_id) {
            return streamer.clone();
        }

        let streamer = Arc::new(TUIStreamer::new(job_id, typescript_path));
        streamers.insert(job_id.to_string(), streamer.clone());
        info!("Created new TUI streamer for job {}", job_id);
        streamer
    }

    /// Return the streamer for a job, or None if not found.
    pub async fn get(&self, job_id: &str) -> Option<Arc<TUIStreamer>> {
        self.streamers.read().await.get(job_id).cloned()
    }

    /// Stop and remove the streamer for a job.
    pub async fn remove(&self, job_id: &str) {
        let mut streamers = self.streamers.write().await;
        if let Some(streamer) = streamers.remove(job_id) {
            streamer.stop();
            info!("Removed TUI streamer for job {}", job_id);
        }
    }

    /// Return the number of active streamers.
    pub async fn active_count(&self) -> usize {
        self.streamers.read().await.len()
    }
}
