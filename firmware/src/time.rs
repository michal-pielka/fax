//! Clock synchronisation.
//!
//! This exists entirely for TLS. Certificate validation checks notBefore and
//! notAfter, and the ESP32 has no battery-backed clock -- it boots believing
//! it is 1970, so every certificate looks "not yet valid" and every handshake
//! fails with an error that never mentions time.
//!
//! So the clock must be right before the first MQTT connection, not eventually.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use esp_idf_svc::sntp::{EspSntp, SyncStatus};
use esp_idf_svc::sys::EspError;

/// Anything before this means the clock has not been set yet.
/// 2024-01-01, comfortably in the past but far from the epoch.
const PLAUSIBLE: Duration = Duration::from_secs(1_704_067_200);

/// Blocks until the clock looks real, then returns the handle.
///
/// The handle must be kept alive: dropping it stops the SNTP client, and the
/// clock then drifts with nothing to correct it.
pub fn sync_blocking(timeout: Duration) -> Result<EspSntp<'static>, EspError> {
    let sntp = EspSntp::new_default()?;

    log::info!("waiting for clock");

    let deadline = std::time::Instant::now() + timeout;

    while std::time::Instant::now() < deadline {
        // Both conditions matter. The status alone can report Completed on a
        // resync before the first set, and the epoch check alone would accept
        // a clock that happens to be stale rather than unset.
        if sntp.get_sync_status() == SyncStatus::Completed && now_since_epoch() > PLAUSIBLE {
            log::info!("clock synced");
            return Ok(sntp);
        }

        std::thread::sleep(Duration::from_millis(200));
    }

    // Deliberately not an error. A wrong clock breaks TLS, but returning here
    // lets the caller log something honest and let the handshake fail with its
    // own message, rather than the device dying silently at boot.
    log::warn!("clock not synced after {timeout:?}; TLS will probably fail");

    Ok(sntp)
}

fn now_since_epoch() -> Duration {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or(Duration::ZERO)
}
