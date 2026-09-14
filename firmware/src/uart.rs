//! The printer over its 5-pin TTL header, at 9600 baud: 960 bytes a second,
//! the slowest link in the whole system and the reason photos stutter. The
//! USB transport that is to replace it implements the same `Transport`.

use std::time::Duration;

use esp_idf_svc::hal::delay::{TickType, BLOCK};
use esp_idf_svc::hal::gpio::{AnyIOPin, InputPin, OutputPin};
use esp_idf_svc::hal::uart::{config::Config as UartConfig, Uart, UartDriver};
use esp_idf_svc::hal::units::Hertz;
use esp_idf_svc::sys::{EspError, ESP_ERR_TIMEOUT};

use crate::printer::Transport;

/// Bytes handed to the driver at a time: a quarter second of wire.
const CHUNK: usize = 256;

pub struct Serial<'d> {
    uart: UartDriver<'d>,
    baud: u32,
}

/// Pin 1 NC, 2 TX -> our RX, 3 RX <- our TX, 4 DTR, 5 GND. DTR is left
/// unread: measured, it tracks neither the paper nor the buffer.
pub fn open<'d, U: Uart + 'd>(
    uart: U,
    tx: impl OutputPin + 'd,
    rx: impl InputPin + 'd,
    baud: u32,
) -> Result<Serial<'d>, EspError> {
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

    Ok(Serial { uart, baud })
}

impl Transport for Serial<'_> {
    fn write(&self, data: &[u8]) -> Result<(), EspError> {
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

    fn read_byte(&self, timeout: Duration) -> Result<Option<u8>, EspError> {
        let mut buf = [0u8; 1];

        // The driver reports an empty wait as a timeout error; here that is
        // an answer, not a failure.
        match self.uart.read(&mut buf, TickType::new_millis(timeout.as_millis() as u64).ticks()) {
            Ok(1) => Ok(Some(buf[0])),
            Ok(_) => Ok(None),
            Err(e) if e.code() == ESP_ERR_TIMEOUT => Ok(None),
            Err(e) => Err(e),
        }
    }

    fn discard_input(&self) -> Result<(), EspError> {
        self.uart.clear_rx()
    }

    /// Ten bits a byte at the baud rate.
    fn transfer_time(&self, bytes: usize) -> Duration {
        Duration::from_micros(bytes as u64 * 10_000_000 / self.baud as u64)
    }
}
