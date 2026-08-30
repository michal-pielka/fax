//! Clock synchronisation, entirely for TLS. The board boots believing it is
//! 1970, so every certificate looks "not yet valid" and no error mentions time.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

use esp_idf_svc::sntp::{EspSntp, SyncStatus};
use esp_idf_svc::sys::EspError;

/// Anything before this means the clock has not been set yet.
/// 2024-01-01, comfortably in the past but far from the epoch.
const PLAUSIBLE: Duration = Duration::from_secs(1_704_067_200);

/// Blocks until the clock looks real. Keep the handle alive: dropping it stops
/// the SNTP client and the clock drifts uncorrected.
pub fn sync_blocking(timeout: Duration) -> Result<EspSntp<'static>, EspError> {
    let sntp = EspSntp::new_default()?;

    log::info!("waiting for clock");

    let deadline = std::time::Instant::now() + timeout;

    while std::time::Instant::now() < deadline {
        // Both matter: the status can report Completed on a resync before the
        // first set, and the epoch alone would accept a merely stale clock.
        if sntp.get_sync_status() == SyncStatus::Completed && now_since_epoch() > PLAUSIBLE {
            log::info!("clock synced");
            return Ok(sntp);
        }

        std::thread::sleep(Duration::from_millis(200));
    }

    // Not an error: better the handshake fails with its own message than the
    // device dies silently at boot.
    log::warn!("clock not synced after {timeout:?}; TLS will probably fail");

    Ok(sntp)
}

fn now_since_epoch() -> Duration {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or(Duration::ZERO)
}
