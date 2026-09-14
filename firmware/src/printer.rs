//! The thermal printer's protocol: what to send, and how to read what it
//! says back. The server renders the ESC/POS, so the only commands here are
//! the two status queries. How the bytes reach the printer is a `Transport`:
//! today USB (usb.rs), the ESP32-S3 as host to the printer's own USB port,
//! which takes a photo in milliseconds. The 9600 baud serial header that
//! came before took twenty seconds and stuttered on every light row.

use std::time::Duration;

use esp_idf_svc::sys::EspError;

/// A way of moving bytes to the printer and back. Implementations block:
/// `write` returns once the printer has the bytes, not once a buffer has them.
pub trait Transport {
    /// Send a whole job, blocking until the printer has taken every byte.
    /// Over USB the printer holds the bytes back with flow control while it
    /// prints, so this returns only once the job is on paper: the return is
    /// itself the confirmation, and there is no separate "done" to wait for.
    fn write(&self, data: &[u8]) -> Result<(), EspError>;

    /// One byte from the printer, or `None` if none arrived within `timeout`.
    fn read_byte(&self, timeout: Duration) -> Result<Option<u8>, EspError>;

    /// Drop whatever the printer has sent and nobody has read.
    fn discard_input(&self) -> Result<(), EspError>;
}

/// The printer, over some transport.
pub struct Printer<T: Transport> {
    link: T,
}

impl<T: Transport> Printer<T> {
    pub fn new(link: T) -> Self {
        Self { link }
    }

    /// Write a rendered job and block until it is printed. The error is a job
    /// that did not reach paper: the printer off, unplugged, or stalled.
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
}
