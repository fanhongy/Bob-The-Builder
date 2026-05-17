//! Configuration loader for the BTB Service.
//!
//! Reads configuration from a single .env-style file (KEY=value per line).
//! Validates that all required keys are present and converts numeric values.

use anyhow::{Context, Result};
use std::collections::HashMap;
use std::path::Path;
use thiserror::Error;

#[derive(Error, Debug)]
pub enum ConfigError {
    #[error("Configuration file not found: {0}")]
    NotFound(String),
    #[error("Cannot read configuration file: {0}")]
    ReadError(String),
    #[error("Missing required configuration value(s): {0}")]
    MissingKeys(String),
    #[error("Configuration value for {key} must be an integer, got: {value}")]
    InvalidInt { key: String, value: String },
}

/// BTB Service configuration loaded from an env file.
#[derive(Debug, Clone)]
pub struct Config {
    pub webhook_secret: String,
    pub github_token: String,
    pub queue_dir: String,
    pub completed_dir: String,
    pub jobs_dir: String,
    pub logs_dir: String,
    pub btb_path: String,
    pub port: u16,
    pub tls_cert: String,
    pub tls_key: String,
    pub job_timeout: u64,
    pub log_retention_days: u64,
    pub aws_profile: String,
    /// EC2 worker mode (optional - if set, jobs run on a remote EC2 instance)
    pub worker_instance_id: Option<String>,
    pub worker_region: Option<String>,
}

const REQUIRED_KEYS: &[&str] = &[
    "WEBHOOK_SECRET",
    "GITHUB_TOKEN",
    "QUEUE_DIR",
    "COMPLETED_DIR",
    "JOBS_DIR",
    "LOGS_DIR",
    "BTB_PATH",
    "PORT",
    "TLS_CERT",
    "TLS_KEY",
    "JOB_TIMEOUT",
    "LOG_RETENTION_DAYS",
    "AWS_PROFILE",
];

const INT_KEYS: &[&str] = &["PORT", "JOB_TIMEOUT", "LOG_RETENTION_DAYS"];

/// Parse a .env-style file into a HashMap.
///
/// Format: KEY=value per line. Lines starting with # are comments.
/// Empty lines and lines with only whitespace are ignored.
/// Values may optionally be quoted with single or double quotes.
fn parse_env_file(path: &str) -> Result<HashMap<String, String>, ConfigError> {
    let config_path = Path::new(path);
    if !config_path.exists() {
        return Err(ConfigError::NotFound(path.to_string()));
    }

    let text = std::fs::read_to_string(config_path)
        .map_err(|e| ConfigError::ReadError(format!("{}: {}", path, e)))?;

    let mut result = HashMap::new();

    for line in text.lines() {
        let stripped = line.trim();
        // Skip empty lines and comments
        if stripped.is_empty() || stripped.starts_with('#') {
            continue;
        }
        // Skip lines without =
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

    Ok(result)
}

/// Load and validate configuration from an env file.
pub fn load_config(path: &str) -> Result<Config, ConfigError> {
    let raw = parse_env_file(path)?;

    // Check for missing required keys
    let missing: Vec<&str> = REQUIRED_KEYS
        .iter()
        .filter(|&&key| !raw.contains_key(key) || raw[key].is_empty())
        .copied()
        .collect();

    if !missing.is_empty() {
        return Err(ConfigError::MissingKeys(missing.join(", ")));
    }

    // Convert numeric values
    let port: u16 = raw["PORT"].parse().map_err(|_| ConfigError::InvalidInt {
        key: "PORT".to_string(),
        value: raw["PORT"].clone(),
    })?;

    let job_timeout: u64 = raw["JOB_TIMEOUT"]
        .parse()
        .map_err(|_| ConfigError::InvalidInt {
            key: "JOB_TIMEOUT".to_string(),
            value: raw["JOB_TIMEOUT"].clone(),
        })?;

    let log_retention_days: u64 = raw["LOG_RETENTION_DAYS"]
        .parse()
        .map_err(|_| ConfigError::InvalidInt {
            key: "LOG_RETENTION_DAYS".to_string(),
            value: raw["LOG_RETENTION_DAYS"].clone(),
        })?;

    let worker_instance_id = raw.get("WORKER_INSTANCE_ID").and_then(|v| {
        if v.is_empty() {
            None
        } else {
            Some(v.clone())
        }
    });

    let worker_region = raw.get("WORKER_REGION").and_then(|v| {
        if v.is_empty() {
            None
        } else {
            Some(v.clone())
        }
    });

    Ok(Config {
        webhook_secret: raw["WEBHOOK_SECRET"].clone(),
        github_token: raw["GITHUB_TOKEN"].clone(),
        queue_dir: raw["QUEUE_DIR"].clone(),
        completed_dir: raw["COMPLETED_DIR"].clone(),
        jobs_dir: raw["JOBS_DIR"].clone(),
        logs_dir: raw["LOGS_DIR"].clone(),
        btb_path: raw["BTB_PATH"].clone(),
        port,
        tls_cert: raw["TLS_CERT"].clone(),
        tls_key: raw["TLS_KEY"].clone(),
        job_timeout,
        log_retention_days,
        aws_profile: raw["AWS_PROFILE"].clone(),
        worker_instance_id,
        worker_region,
    })
}
