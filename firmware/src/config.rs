//! Everything you would change without changing behaviour. Credentials are
//! baked in via env!, so a missing one is a compile error rather than a bad flash.

use std::time::Duration;

pub const WIFI_SSID: &str = env!("WIFI_SSID");
pub const WIFI_PASS: &str = env!("WIFI_PASS");

/// Websockets, because Caddy cannot proxy plain MQTT and this keeps the
/// certificate its problem. A hostname, so moving the VPS is not a reflash.
pub const MQTT_URL: &str = "wss://fax.pielka.sh/mqtt";
pub const MQTT_PASS: &str = env!("MQTT_PASS");
pub const MQTT_USER: &str = "printer";

/// The device id, and so every topic. Must match DEVICE_ID on the server.
/// Drift and jobs vanish with no error anywhere, which is why main logs the
/// topics at boot. A macro, since concat! takes only literals.
macro_rules! device_id {
    () => {
        "printer-1"
    };
}

pub const CLIENT_ID: &str = concat!("fax-", device_id!());
pub const JOB_TOPIC: &str = concat!("fax/", device_id!(), "/job/+");
pub const STATE_TOPIC: &str = concat!("fax/", device_id!(), "/state");

/// A prefix; the job id is appended. The dispatcher matches by topic, so it
/// never has to parse the payload.
pub const ACK_TOPIC: &str = concat!("fax/", device_id!(), "/ack");

/// Published, retained, on every connect. Paper is not part of the state: it
/// is measured per job, right before the bytes go out, and answered in the ack.
pub const ONLINE: &[u8] = br#"{"online":true}"#;

/// The last will: published by the broker if we vanish without disconnecting.
pub const OFFLINE: &[u8] = br#"{"online":false}"#;

/// What an ack says. The reasons are codes rather than prose because the
/// dispatcher switches on them -- a sentence would mean matching strings.
pub const ACK_OK: &[u8] = br#"{"ok":true}"#;
pub const ACK_NO_PAPER: &[u8] = br#"{"ok":false,"error":"no_paper"}"#;
pub const ACK_NO_CONFIRM: &[u8] = br#"{"ok":false,"error":"no_confirmation"}"#;

/// How long a job waits for the printer to be attached before giving up on
/// it: long enough for a printer switched on as the job arrives to enumerate.
pub const ATTACH_WAIT: Duration = Duration::from_secs(10);

/// The most one job may take, from arrival to ack, attach wait included. A
/// photo prints in about a second over USB, so this only bites on a printer
/// that is missing or stalled. Cancelling a transfer caught mid-flight can
/// add up to two seconds more. backend/internal/timeouts mirrors this, and
/// waits longer, so a printer that gives up gets to say why.
pub const JOB_DEADLINE: Duration = Duration::from_secs(20);

/// The largest job we will assemble, in bytes. Matches the broker's
/// max_packet_size: anything the broker lets through fits, and a claimed size
/// beyond it is refused before a byte is allocated. A square photo is ~19 KB.
pub const MAX_JOB: usize = 32 * 1024;
