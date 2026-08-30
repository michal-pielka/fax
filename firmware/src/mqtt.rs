//! The broker connection. The rule: the ESP-IDF MQTT task is BLOCKED while our
//! callback runs, so the callback only hands events to the main thread.

use std::sync::mpsc::{self, Receiver};

use esp_idf_svc::mqtt::client::{
    EspMqttClient, EventPayload, LwtConfiguration, MqttClientConfiguration, QoS,
};
use esp_idf_svc::sys::{esp_crt_bundle_attach, EspError};

use crate::config;

/// What the callback sends to the main thread.
pub enum Event {
    /// (Re)connected. Subscriptions do not survive a reconnect, so the main
    /// thread has to resubscribe every time this arrives, not just once.
    Connected,
    /// A finished job. The id comes from the topic, which keeps the payload
    /// pure ESC/POS, and travels back out on the ack.
    Job { id: String, payload: Vec<u8> },
}

/// Connects, returning the client so the main thread can subscribe and
/// publish, and the receiver so it learns anything happened.
pub fn connect() -> Result<(EspMqttClient<'static>, Receiver<Event>), EspError> {
    let options = MqttClientConfiguration {
        client_id: Some(config::CLIENT_ID),
        username: Some(config::MQTT_USER),
        password: Some(config::MQTT_PASS),

        // Clean session: the broker must NOT hold jobs for us while we are
        // unplugged, or reconnecting would print hours of backlog at once.
        disable_clean_session: false,

        // The built-in Mozilla bundle, rather than a pinned cert that would
        // break on renewal. Only matters if MQTT_URL is mqtts://.
        crt_bundle_attach: Some(esp_crt_bundle_attach),

        lwt: Some(LwtConfiguration {
            topic: config::STATE_TOPIC,
            payload: config::OFFLINE,
            qos: QoS::AtLeastOnce,
            retain: true,
        }),

        ..Default::default()
    };

    // Bounded: the main thread needs half a second per job, so anything
    // faster grows heap forever on a board with 300 KB of it.
    let (tx, rx) = mpsc::sync_channel(4);

    let client = EspMqttClient::new_cb(config::MQTT_URL, &options, move |event| {
        match event.payload() {
            // try_send, never send: send blocks when full, which blocks the
            // MQTT task and stalls the PUBACK.
            EventPayload::Connected(_) => {
                if tx.try_send(Event::Connected).is_err() {
                    log::error!("channel full, dropped a connect -- not subscribed");
                }
            }

            // One topic subscribed, so this is a job. No chunk reassembly:
            // raise the 255-character cap past ~3500 and receipts will split.
            EventPayload::Received { topic, data, .. } => {
                // No topic means a continuation chunk, which cannot happen
                // at our sizes. Say so and drop it rather than reassemble.
                let Some(id) = topic.and_then(|t| t.rsplit('/').next()) else {
                    log::warn!("job with no topic, dropped");
                    return;
                };

                // No queue in this design: shed work rather than exhaust the
                // heap and reboot mid-receipt.
                let job = Event::Job {
                    id: id.to_string(),
                    payload: data.to_vec(),
                };

                if tx.try_send(job).is_err() {
                    log::warn!("channel full, dropped job {id}");
                }
            }

            EventPayload::Disconnected => log::warn!("disconnected from broker"),

            EventPayload::Error(e) => log::error!("mqtt error: {e}"),

            _ => {}
        }
    })?;

    Ok((client, rx))
}
