//! Data models for the BTB Service.
//!
//! Defines the Job and PushResult structs used throughout the service.
//! Both support JSON serialization for disk persistence.

use serde::{Deserialize, Serialize};

/// Result of pushing btb results back to the original repository.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PushResult {
    pub success: bool,
    /// "btb-results/{original-branch}"
    pub branch: String,
    /// Error message if push failed
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
}

/// Represents a single btb execution job.
///
/// A Job is created when a GitHub webhook push event is received for a
/// repository containing a .btb file. It tracks the full lifecycle from
/// submission through execution, result push-back, and cleanup.
///
/// Job statuses: "pending", "running", "completed", "failed", "timeout", "stopped"
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Job {
    /// UUID
    pub id: String,
    /// GitHub clone URL
    pub repo_url: String,
    /// Branch name
    pub branch: String,
    /// Commit SHA
    pub commit_sha: String,
    /// GitHub username of pusher
    pub pusher: String,
    /// Spec to run (read from .btb file)
    pub spec_name: String,
    /// "pending" | "running" | "completed" | "failed" | "timeout" | "stopped"
    pub status: String,
    /// ISO 8601 timestamp
    pub submitted_at: String,
    /// ISO 8601 timestamp
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started_at: Option<String>,
    /// ISO 8601 timestamp
    #[serde(skip_serializing_if = "Option::is_none")]
    pub completed_at: Option<String>,
    /// btb exit code
    #[serde(skip_serializing_if = "Option::is_none")]
    pub exit_code: Option<i32>,
    /// Error message if failed
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    /// "btb-results/{branch}" after push
    #[serde(skip_serializing_if = "Option::is_none")]
    pub results_branch: Option<String>,
    /// Whether result push succeeded
    #[serde(skip_serializing_if = "Option::is_none")]
    pub push_success: Option<bool>,
    /// Error message if push failed
    #[serde(skip_serializing_if = "Option::is_none")]
    pub push_error: Option<String>,
    /// Whether working dir cleanup succeeded
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cleanup_success: Option<bool>,
    /// Job ID of the original job if this is a retry
    #[serde(skip_serializing_if = "Option::is_none")]
    pub retry_of: Option<String>,
    /// ISO 8601 timestamp when stopped
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stopped_at: Option<String>,
    /// If true, don't cleanup workdir on stop
    #[serde(default)]
    pub preserve_workdir: bool,
}

impl Job {
    /// Serialize this Job to a JSON string.
    pub fn to_json(&self) -> anyhow::Result<String> {
        Ok(serde_json::to_string_pretty(self)?)
    }

    /// Deserialize a Job from a JSON string.
    pub fn from_json(json_str: &str) -> anyhow::Result<Self> {
        Ok(serde_json::from_str(json_str)?)
    }
}

impl PushResult {
    /// Serialize this PushResult to a JSON string.
    pub fn to_json(&self) -> anyhow::Result<String> {
        Ok(serde_json::to_string_pretty(self)?)
    }

    /// Deserialize a PushResult from a JSON string.
    pub fn from_json(json_str: &str) -> anyhow::Result<Self> {
        Ok(serde_json::from_str(json_str)?)
    }
}
