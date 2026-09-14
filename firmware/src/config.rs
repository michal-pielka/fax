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
pub const CLIENT_ID: &str = "fax-printer-1";

/// Must match DEVICE_ID on the server. Drift and jobs vanish with no error
/// anywhere, which is why main logs these at boot.
pub const JOB_TOPIC: &str = "fax/printer-1/job/+";
pub const STATE_TOPIC: &str = "fax/printer-1/state";

/// A prefix; the job id is appended. The dispatcher matches by topic, so it
/// never has to parse the payload.
pub const ACK_TOPIC: &str = "fax/printer-1/ack";

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

/// The largest job we will assemble, in bytes. Matches the broker's
/// max_packet_size: anything the broker lets through fits, and a claimed size
/// beyond it is refused before a byte is allocated. A square photo is ~19 KB.
pub const MAX_JOB: usize = 32 * 1024;

/// Printed on the printer's self-test page (hold feed, then power on).
pub const BAUD_RATE: u32 = 9600;

/// How long to wait for the printer's status byte after a job. It answers as
/// soon as it has parsed the bytes, normally within milliseconds; the browser
/// is waiting on this, so a silent printer gets seconds, not a minute. The
/// dispatcher's ackTimeout is two seconds longer, so a printer that gives up
/// gets to say why.
pub const PRINT_TIMEOUT: Duration = Duration::from_secs(5);
