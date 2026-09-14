//! The thermal printer's protocol: what to send, and how to read what it
//! says back. The server renders the ESC/POS, so the only commands here are
//! the two status queries. How the bytes reach the printer is a `Transport`:
//! today USB (usb.rs), the ESP32-S3 as host to the printer's own USB port,
//! which takes a photo in milliseconds. The 9600 baud serial header that
//! came before took twenty seconds and stuttered on every light row.

use std::time::{Duration, Instant};

use esp_idf_svc::sys::EspError;

/// A way of moving bytes to the printer and back. Implementations block:
/// `write` returns once the printer has the bytes, not once a buffer has them.
pub trait Transport {
    /// Send a whole job, blocking until it has left this side.
    fn write(&self, data: &[u8]) -> Result<(), EspError>;

    /// One byte from the printer, or `None` if none arrived within `timeout`.
    fn read_byte(&self, timeout: Duration) -> Result<Option<u8>, EspError>;

    /// Drop whatever the printer has sent and nobody has read.
    fn discard_input(&self) -> Result<(), EspError>;

    /// How long `bytes` take to reach the printer over this transport. The
    /// status wait after a job allows this on top of a fixed margin, since a
    /// printer still receiving cannot have answered yet. Over USB it is next
    /// to nothing; it existed for a serial wire.
    fn transfer_time(&self, bytes: usize) -> Duration;
}

/// The printer, over some transport.
pub struct Printer<T: Transport> {
    link: T,
}

/// The fixed part of the wait for a status byte: how long a printer that has
/// the whole job may still take to answer. The dispatcher's ackTimeout allows
/// this plus two seconds, so a printer that gives up gets to say why.
const STATUS_WAIT: Duration = Duration::from_secs(5);

impl<T: Transport> Printer<T> {
    pub fn new(link: T) -> Self {
        Self { link }
    }

    /// Write a rendered job and block until the printer has it.
    pub fn write(&self, data: &[u8]) -> Result<(), EspError> {
        self.link.write(data)
    }

    /// Ask whether the printer has paper. `None` is "did not answer", not
    /// "no paper". DLE EOT 4 is real-time: the printer answers at once, even
    /// mid-job.
    pub fn has_paper(&self) -> Option<bool> {
        // Anything already buffered would be read as this query's reply.
        self.link.discard_input().ok()?;
        self.link.write(&[0x10, 0x04, 0x04]).ok()?;

        let status = self.link.read_byte(Duration::from_millis(300)).ok()??;

        // Bits 1 and 4 are set in every status byte. If they are not, this is
        // a stray byte and guessing from it is worse than admitting we do not
        // know.
        if status & 0b0001_0010 != 0b0001_0010 {
            log::warn!("printer answered {status:#04x}, not a status byte");
            return None;
        }

        // Bits 5 and 6 are the paper-end sensor: 00 present, 11 gone.
        // Measured on this printer as 0x12 with the roll in, 0x72 with it out.
        Some(status & 0b0110_0000 == 0)
    }

    /// Wait for the printer to answer for a job of `job_bytes` just written.
    /// `false` means it never did: jammed, unplugged, or silent. The answer
    /// comes once the printer has parsed everything before the query, which
    /// is milliseconds after the last byte arrives.
    ///
    /// The reply is one byte, but a stray byte can arrive first; anything
    /// that is not a status byte is read past rather than taken as a refusal.
    pub fn wait_done(&self, job_bytes: usize) -> bool {
        if self.link.write(&[0x1D, 0x72, 0x01]).is_err() {
            return false;
        }

        let deadline = Instant::now() + STATUS_WAIT + self.link.transfer_time(job_bytes);

        while let Some(left) = deadline.checked_duration_since(Instant::now()) {
            let status = match self.link.read_byte(left) {
                Ok(Some(b)) => b,
                Ok(None) => return false,
                Err(_) => return false,
            };

            // GS r has no fixed bits, so this is weak: only the low nibble is
            // defined, and this printer answers 0x00 with paper and 0x0c
            // without.
            if status & 0b1111_0000 != 0 {
                log::warn!("printer said {status:#04x} while we waited for a status; still waiting");
                continue;
            }

            return true;
        }

        false
    }
}
