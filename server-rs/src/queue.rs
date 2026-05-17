//! Disk-based FIFO job queue for the BTB Service.
//!
//! Jobs are persisted as JSON files in a queue directory, named with a
//! timestamp prefix for natural FIFO ordering when sorted alphabetically.
//! A file-based lock prevents race conditions during dequeue operations.
//! Completed jobs are moved to a separate completed directory.

use crate::models::Job;
use anyhow::{Context, Result};
use chrono::Utc;
use fs2::FileExt;
use std::fs;
use std::path::{Path, PathBuf};
use tracing::{info, warn};

/// Default lock file path; can be overridden via constructor.
const DEFAULT_LOCK_FILE: &str = "/var/btb/.queue-lock";

/// Disk-based FIFO job queue.
///
/// Jobs are stored as JSON files named `{timestamp}_{job_id}.json`
/// in the queue directory. The timestamp prefix ensures natural FIFO
/// ordering when the directory listing is sorted alphabetically.
///
/// A file-based lock serializes dequeue operations so that two concurrent
/// pollers cannot grab the same job.
pub struct JobQueue {
    pub queue_dir: PathBuf,
    pub completed_dir: PathBuf,
    pub jobs_dir: PathBuf,
    lock_file: PathBuf,
}

impl JobQueue {
    /// Create a new JobQueue with the given directories.
    pub fn new(
        queue_dir: &str,
        completed_dir: &str,
        jobs_dir: &str,
        lock_file: Option<&str>,
    ) -> Result<Self> {
        let queue_dir = PathBuf::from(queue_dir);
        let completed_dir = PathBuf::from(completed_dir);
        let jobs_dir = PathBuf::from(jobs_dir);
        let lock_file = PathBuf::from(lock_file.unwrap_or(DEFAULT_LOCK_FILE));

        // Create directories if they don't exist
        fs::create_dir_all(&queue_dir).context("Failed to create queue directory")?;
        fs::create_dir_all(&completed_dir).context("Failed to create completed directory")?;
        fs::create_dir_all(&jobs_dir).context("Failed to create jobs directory")?;
        if let Some(parent) = lock_file.parent() {
            fs::create_dir_all(parent).context("Failed to create lock file directory")?;
        }

        Ok(Self {
            queue_dir,
            completed_dir,
            jobs_dir,
            lock_file,
        })
    }

    /// Generate a queue filename for a job.
    fn make_filename(&self, job: &Job) -> String {
        let timestamp = Utc::now().format("%Y%m%d%H%M%S%f").to_string();
        format!("{}_{}.json", timestamp, job.id)
    }

    /// Read and deserialize a Job from a JSON file on disk.
    fn read_job_file(path: &Path) -> Result<Job> {
        let content = fs::read_to_string(path)
            .with_context(|| format!("Failed to read job file: {:?}", path))?;
        Job::from_json(&content)
    }

    /// Serialize and write a Job to a JSON file on disk.
    fn write_job_file(path: &Path, job: &Job) -> Result<()> {
        let json = job.to_json()?;
        fs::write(path, json)
            .with_context(|| format!("Failed to write job file: {:?}", path))?;
        Ok(())
    }

    /// Return queue directory JSON files sorted by filename (FIFO order).
    pub fn sorted_queue_files(&self) -> Vec<PathBuf> {
        if !self.queue_dir.exists() {
            return Vec::new();
        }

        let mut files: Vec<PathBuf> = fs::read_dir(&self.queue_dir)
            .into_iter()
            .flatten()
            .flatten()
            .map(|entry| entry.path())
            .filter(|p| p.extension().map_or(false, |ext| ext == "json"))
            .collect();

        files.sort_by(|a, b| a.file_name().cmp(&b.file_name()));
        files
    }

    /// Return completed directory JSON files sorted by filename (most recent first).
    fn sorted_completed_files(&self) -> Vec<PathBuf> {
        if !self.completed_dir.exists() {
            return Vec::new();
        }

        let mut files: Vec<PathBuf> = fs::read_dir(&self.completed_dir)
            .into_iter()
            .flatten()
            .flatten()
            .map(|entry| entry.path())
            .filter(|p| p.extension().map_or(false, |ext| ext == "json"))
            .collect();

        files.sort_by(|a, b| b.file_name().cmp(&a.file_name()));
        files
    }

    /// Find a job file in the queue directory by job ID.
    fn find_job_file_in_queue(&self, job_id: &str) -> Option<PathBuf> {
        let suffix = format!("_{}.json", job_id);
        self.sorted_queue_files()
            .into_iter()
            .find(|f| {
                f.file_name()
                    .map_or(false, |name| name.to_string_lossy().ends_with(&suffix))
            })
    }

    /// Find a job file in the completed directory by job ID.
    fn find_job_file_in_completed(&self, job_id: &str) -> Option<PathBuf> {
        let suffix = format!("_{}.json", job_id);
        self.sorted_completed_files()
            .into_iter()
            .find(|f| {
                f.file_name()
                    .map_or(false, |name| name.to_string_lossy().ends_with(&suffix))
            })
    }

    /// Add a job to the queue.
    pub fn enqueue(&self, job: &Job) -> Result<String> {
        let filename = self.make_filename(job);
        let filepath = self.queue_dir.join(&filename);
        Self::write_job_file(&filepath, job)?;
        info!("Enqueued job {} as {}", job.id, filename);
        Ok(job.id.clone())
    }

    /// Dequeue the next pending job.
    ///
    /// Acquires a file-based lock, scans the queue directory for the
    /// first job with status "pending", updates it to "running"
    /// with a started_at timestamp, writes it back, and returns it.
    pub fn dequeue(&self) -> Result<Option<Job>> {
        let lock_fd = fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(false)
            .open(&self.lock_file)
            .context("Failed to open lock file")?;

        lock_fd.lock_exclusive().context("Failed to acquire lock")?;

        let result = (|| -> Result<Option<Job>> {
            for filepath in self.sorted_queue_files() {
                let job = match Self::read_job_file(&filepath) {
                    Ok(j) => j,
                    Err(_) => {
                        warn!("Failed to read job file {:?}, skipping", filepath);
                        continue;
                    }
                };

                if job.status == "pending" {
                    let mut job = job;
                    job.status = "running".to_string();
                    job.started_at = Some(Utc::now().to_rfc3339());
                    Self::write_job_file(&filepath, &job)?;
                    info!("Dequeued job {}", job.id);
                    return Ok(Some(job));
                }
            }
            Ok(None)
        })();

        lock_fd.unlock().context("Failed to release lock")?;
        result
    }

    /// Get the currently running job, if any.
    pub fn get_running(&self) -> Option<Job> {
        for filepath in self.sorted_queue_files() {
            if let Ok(job) = Self::read_job_file(&filepath) {
                if job.status == "running" {
                    return Some(job);
                }
            }
        }
        None
    }

    /// Get all pending jobs in FIFO order.
    pub fn get_pending(&self) -> Vec<Job> {
        let mut pending = Vec::new();
        for filepath in self.sorted_queue_files() {
            if let Ok(job) = Self::read_job_file(&filepath) {
                if job.status == "pending" {
                    pending.push(job);
                }
            }
        }
        pending
    }

    /// Get recently completed jobs.
    pub fn get_completed(&self, limit: usize) -> Vec<Job> {
        let mut completed = Vec::new();
        for filepath in self.sorted_completed_files() {
            if completed.len() >= limit {
                break;
            }
            if let Ok(job) = Self::read_job_file(&filepath) {
                completed.push(job);
            }
        }
        completed
    }

    /// Mark a job as complete and move it to the completed directory.
    pub fn complete(
        &self,
        job_id: &str,
        status: &str,
        exit_code: i32,
        error: Option<&str>,
        results_branch: Option<&str>,
        push_success: Option<bool>,
        push_error: Option<&str>,
        cleanup_success: Option<bool>,
    ) -> Result<()> {
        let filepath = self
            .find_job_file_in_queue(job_id)
            .ok_or_else(|| anyhow::anyhow!("Job file for {} not found in queue directory", job_id))?;

        let mut job = Self::read_job_file(&filepath)?;
        job.status = status.to_string();
        job.completed_at = Some(Utc::now().to_rfc3339());
        job.exit_code = Some(exit_code);
        if let Some(e) = error {
            job.error = Some(e.to_string());
        }
        if let Some(rb) = results_branch {
            job.results_branch = Some(rb.to_string());
        }
        if let Some(ps) = push_success {
            job.push_success = Some(ps);
        }
        if let Some(pe) = push_error {
            job.push_error = Some(pe.to_string());
        }
        if let Some(cs) = cleanup_success {
            job.cleanup_success = Some(cs);
        }

        // Move to completed directory, preserving the filename
        let filename = filepath.file_name().unwrap();
        let dest = self.completed_dir.join(filename);
        Self::write_job_file(&dest, &job)?;
        fs::remove_file(&filepath)?;

        info!(
            "Completed job {} with status={} exit_code={}",
            job_id, status, exit_code
        );
        Ok(())
    }

    /// Find a job by ID, searching both queue and completed directories.
    pub fn get_job(&self, job_id: &str) -> Option<Job> {
        // Search queue directory first
        if let Some(filepath) = self.find_job_file_in_queue(job_id) {
            if let Ok(job) = Self::read_job_file(&filepath) {
                return Some(job);
            }
        }

        // Search completed directory
        if let Some(filepath) = self.find_job_file_in_completed(job_id) {
            if let Ok(job) = Self::read_job_file(&filepath) {
                return Some(job);
            }
        }

        None
    }

    /// Get stopped jobs that still have preserved working directories.
    pub fn get_stopped(&self, limit: usize) -> Vec<Job> {
        let mut stopped = Vec::new();
        for filepath in self.sorted_queue_files() {
            if stopped.len() >= limit {
                break;
            }
            if let Ok(job) = Self::read_job_file(&filepath) {
                if job.status == "stopped" {
                    stopped.push(job);
                }
            }
        }
        stopped
    }

    /// Update a job's fields in place.
    pub fn update_job(&self, job_id: &str, updates: serde_json::Value) -> Option<Job> {
        let filepath = self
            .find_job_file_in_queue(job_id)
            .or_else(|| self.find_job_file_in_completed(job_id))?;

        let mut job = Self::read_job_file(&filepath).ok()?;

        // Apply updates from a JSON value
        if let Some(obj) = updates.as_object() {
            let mut job_value = serde_json::to_value(&job).ok()?;
            if let Some(job_obj) = job_value.as_object_mut() {
                for (key, value) in obj {
                    job_obj.insert(key.clone(), value.clone());
                }
            }
            job = serde_json::from_value(job_value).ok()?;
        }

        Self::write_job_file(&filepath, &job).ok()?;
        Some(job)
    }

    /// Delete a job from the queue or completed directory.
    pub fn delete_job(&self, job_id: &str) -> bool {
        let filepath = self
            .find_job_file_in_queue(job_id)
            .or_else(|| self.find_job_file_in_completed(job_id));

        if let Some(path) = filepath {
            if fs::remove_file(&path).is_ok() {
                info!("Deleted job {}", job_id);
                return true;
            }
        }
        false
    }

    /// Move a job from completed back to queue (for resume).
    pub fn move_to_queue(&self, job_id: &str) -> bool {
        // Check if already in queue
        if self.find_job_file_in_queue(job_id).is_some() {
            return false;
        }

        let filepath = match self.find_job_file_in_completed(job_id) {
            Some(p) => p,
            None => return false,
        };

        let job = match Self::read_job_file(&filepath) {
            Ok(j) => j,
            Err(_) => return false,
        };

        let dest = self.queue_dir.join(filepath.file_name().unwrap());
        if Self::write_job_file(&dest, &job).is_ok() && fs::remove_file(&filepath).is_ok() {
            info!("Moved job {} back to queue", job_id);
            return true;
        }
        false
    }
}
