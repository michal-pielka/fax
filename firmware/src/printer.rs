//! The thermal printer, over UART. The server renders the commands, so new
//! formatting needs no reflash. The exception is the status queries below.

use std::time::{Duration, Instant};

use esp_idf_svc::hal::delay::{TickType, BLOCK};
use esp_idf_svc::hal::gpio::{AnyIOPin, InputPin, OutputPin};
use esp_idf_svc::hal::uart::{config::Config as UartConfig, Uart, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::sys::{EspError, ESP_ERR_TIMEOUT};

/// Bytes handed to the driver at a time: a quarter second of wire.
const CHUNK: usize = 256;

/// The printer's serial line.
pub struct Port<'d> {
    uart: UartDriver<'d>,
}

/// The 5-pin TTL header: 1 NC, 2 TX -> GPIO16, 3 RX <- GPIO17, 4 DTR, 5 GND.
/// DTR is left unconnected in software: measured, it tracks neither the paper
/// nor the buffer, staying low through a 19-second job. UART0 is the console.
pub fn open<'d, U: Uart + 'd>(
    uart: U,
    tx: impl OutputPin + 'd,
    rx: impl InputPin + 'd,
    baud: u32,
) -> Result<Port<'d>, EspError> {
    let uart = UartDriver::new(
        uart,
        tx,
        rx,
        Option::<AnyIOPin>::None, // no CTS
        Option::<AnyIOPin>::None, // no RTS
        // The transmit buffer is the driver's, filled by us and drained by
        // an interrupt. The default is 256 bytes, a quarter second at 9600
        // baud, so a task holding the CPU that long starves the wire and the
        // paper stutters. Eight seconds of slack instead.
        &UartConfig::default()
            .baudrate(Hertz(baud))
            .tx_fifo_size(8 * 1024),
    )?;

    Ok(Port { uart })
}

impl Port<'_> {
    /// Write a rendered job and block until it is physically on the wire.
    pub fn write(&self, data: &[u8]) -> Result<(), EspError> {
        for chunk in data.chunks(CHUNK) {
            // Never hand the driver more than its buffer has room for. Given
            // a full buffer, ESP-IDF's write spins on the free-space count
            // instead of sleeping, which starves the idle task and trips the
            // watchdog after five seconds -- a photo takes twenty. Waiting
            // here, with a real sleep, keeps the CPU shared for the job.
            while self.uart.remaining_write()? < chunk.len() {
                std::thread::sleep(Duration::from_millis(20));
            }

            // A single write may be partial, so loop until it has all gone out.
            let mut sent = 0;
            while sent < chunk.len() {
                sent += self.uart.write(&chunk[sent..])?;
            }
        }

        // ESP-IDF returns before the bytes reach the wire, so without this we
        // report a receipt printed from inside a driver buffer.
        self.uart.wait_tx_done(BLOCK)
    }

    /// Ask whether the printer has paper. `None` is "did not answer", not
    /// "no paper". DLE EOT 4 is real-time: the printer answers at once, even
    /// mid-job.
    pub fn has_paper(&self) -> Option<bool> {
        // Anything already buffered would be read as this query's reply.
        self.uart.clear_rx().ok()?;
        self.write(&[0x10, 0x04, 0x04]).ok()?;

        let mut buf = [0u8; 1];
        if self.uart.read(&mut buf, TickType::new_millis(300).ticks()).ok()? != 1 {
            return None;
        }

        let status = buf[0];

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

    /// Wait for the printer to answer for the job just written. `false` means
    /// it never did: jammed, unplugged, or silent. The answer comes once the
    /// printer has worked through everything before the query: milliseconds
    /// for text, and for a photo after the last stored piece has printed.
    ///
    /// The reply is one byte, but a stray byte can arrive first; anything
    /// that is not a status byte is read past rather than taken as a refusal.
    pub fn wait_done(&self, timeout: Duration) -> bool {
        if self.write(&[0x1D, 0x72, 0x01]).is_err() {
            return false;
        }

        let deadline = Instant::now() + timeout;
        let mut buf = [0u8; 1];

        while let Some(left) = deadline.checked_duration_since(Instant::now()) {
            // The driver reports an empty wait as a timeout error; that is the
            // normal case until the answer arrives, not a failure.
            match self.uart.read(&mut buf, TickType::new_millis(left.as_millis() as u64).ticks()) {
                Ok(1) => {}
                Ok(_) => continue,
                Err(e) if e.code() == ESP_ERR_TIMEOUT => return false,
                Err(_) => return false,
            }

            let status = buf[0];

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
