//! The broker connection. The rule: the ESP-IDF MQTT task is BLOCKED while our
//! callback runs, so the callback only hands events to the main thread.

use std::sync::mpsc::{self, Receiver};

use esp_idf_svc::mqtt::client::{
    Details, EspMqttClient, EventPayload, LwtConfiguration, MqttClientConfiguration, QoS,
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

    // Bounded, and small: a job can be 32 KB and the board has 300 KB of
    // heap. The dispatcher sends one job at a time and waits for its ack,
    // so more than one in flight means a redelivery, not a queue.
    let (tx, rx) = mpsc::sync_channel(2);

    // ESP-IDF hands a message over in pieces of its receive buffer, about a
    // kilobyte each. A text receipt is one piece; a photo is twenty. This is
    // the one being put back together, if any. It lives in the callback,
    // which is the only place that sees the pieces.
    let mut partial: Option<Partial> = None;

    let client = EspMqttClient::new_cb(config::MQTT_URL, &options, move |event| {
        match event.payload() {
            // try_send, never send: send blocks when full, which blocks the
            // MQTT task and stalls the PUBACK.
            EventPayload::Connected(_) => {
                if tx.try_send(Event::Connected).is_err() {
                    log::error!("channel full, dropped a connect -- not subscribed");
                }
            }

            // One topic subscribed, so this is a job, or a piece of one. Only
            // the first piece carries the topic; the rest carry an offset.
            EventPayload::Received { topic, data, details, .. } => {
                let payload = match details {
                    Details::Complete => {
                        let Some(id) = topic.and_then(|t| t.rsplit('/').next()) else {
                            log::warn!("job with no topic, dropped");
                            return;
                        };
                        partial = None;
                        Some((id.to_string(), data.to_vec()))
                    }

                    Details::InitialChunk(first) => {
                        let Some(id) = topic.and_then(|t| t.rsplit('/').next()) else {
                            log::warn!("job with no topic, dropped");
                            return;
                        };
                        // Refused before allocating: the claimed size is the
                        // sender's word, and the heap is 300 KB.
                        if first.total_data_size > config::MAX_JOB {
                            log::warn!("job {id} is {} bytes, over the limit, dropped", first.total_data_size);
                            partial = None;
                            return;
                        }
                        let mut buf = Vec::with_capacity(first.total_data_size);
                        buf.extend_from_slice(data);
                        partial = Some(Partial { id: id.to_string(), buf, total: first.total_data_size });
                        None
                    }

                    Details::SubsequentChunk(next) => {
                        let Some(p) = partial.as_mut() else {
                            log::warn!("a piece of a job we never started, dropped");
                            return;
                        };
                        // Pieces arrive in order and back to back. Anything
                        // else means one went missing, and a receipt with a
                        // hole in it is worse than none.
                        if next.current_data_offset != p.buf.len() || next.total_data_size != p.total {
                            log::warn!("job {} arrived out of order, dropped", p.id);
                            partial = None;
                            return;
                        }
                        p.buf.extend_from_slice(data);
                        if p.buf.len() < p.total {
                            return;
                        }
                        partial.take().map(|p| (p.id, p.buf))
                    }
                };

                let Some((id, payload)) = payload else { return };

                // No queue in this design: shed work rather than exhaust the
                // heap and reboot mid-receipt.
                if tx.try_send(Event::Job { id: id.clone(), payload }).is_err() {
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

/// A job still arriving: what we have of it, and how much there will be.
struct Partial {
    id: String,
    buf: Vec<u8>,
    total: usize,
}
