//! The printer over its USB port, with the ESP32-S3 as host. Megabytes a
//! second where the serial header did 960 bytes: a photo is in the printer's
//! buffer before the head has moved, and it prints in one motion. Against
//! ESP-IDF's usb_host C API directly; there is no safe wrapper for it.
//!
//! The printer is a USB printer-class device (class 7) with one bulk-out
//! endpoint for jobs and one bulk-in for its replies. It may be off or
//! unplugged at boot and come and go afterwards; `write` waits a while for it.

use std::collections::VecDeque;
use std::ffi::c_void;
use std::ptr;
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

use esp_idf_svc::sys::*;

use crate::printer::Transport;

const FOREVER: TickType_t = TickType_t::MAX;

/// A bulk transfer this large is one allocation and one submit; the host
/// library packetises it. Jobs are written in pieces of this.
const CHUNK: usize = 4096;

/// How long a job will wait for the printer to be attached before failing.
const ATTACH_WAIT: Duration = Duration::from_secs(10);

/// The printer as currently attached: its handle and the two endpoints.
#[derive(Clone, Copy)]
struct Attached {
    dev: usb_device_handle_t,
    out: u8,
    inp: u8,
}

// Handles are pointers into the host library, shared across our threads
// under a mutex; the library is thread-safe for the calls made here.
unsafe impl Send for Attached {}

pub struct Usb {
    attached: Arc<(Mutex<Option<Attached>>, Condvar)>,
    /// Bytes the printer has sent and nobody has asked for yet: a bulk-in
    /// transfer may bring more than the one byte a caller wants.
    pending: Mutex<VecDeque<u8>>,
}

unsafe impl Send for Usb {}
unsafe impl Sync for Usb {}

/// Start the host and the client, and begin watching for the printer.
pub fn open() -> Result<Usb, EspError> {
    unsafe {
        let mut cfg = usb_host_config_t::default();
        cfg.intr_flags = ESP_INTR_FLAG_LEVEL1 as _;
        esp!(usb_host_install(&cfg))?;

        // The library's own event pump. Runs for the life of the program.
        spawn("usb-lib", 4096, || loop {
            let mut flags = 0u32;
            usb_host_lib_handle_events(FOREVER, &mut flags);
        });

        // The client's callback only forwards; opening and closing devices
        // happens on the thread below, where blocking is allowed.
        let (tx, rx) = mpsc::channel::<Plug>();
        let tx = Box::into_raw(Box::new(tx));

        let mut ccfg = usb_host_client_config_t::default();
        ccfg.is_synchronous = false;
        ccfg.max_num_event_msg = 5;
        ccfg.__bindgen_anon_1.async_.client_event_callback = Some(on_client_event);
        ccfg.__bindgen_anon_1.async_.callback_arg = tx as *mut c_void;

        let mut client: usb_host_client_handle_t = ptr::null_mut();
        esp!(usb_host_client_register(&ccfg, &mut client))?;

        // Transfer completions arrive on this thread too, so nothing here
        // may wait on a transfer.
        let client_addr = client as usize;
        spawn("usb-client", 4096, move || loop {
            usb_host_client_handle_events(client_addr as usb_host_client_handle_t, FOREVER);
        });

        let attached = Arc::new((Mutex::new(None), Condvar::new()));
        let watched = Arc::clone(&attached);
        spawn("usb-plug", 6144, move || {
            for plug in rx {
                let (lock, cv) = &*watched;
                match plug {
                    Plug::New(addr) => match attach(client_addr as usb_host_client_handle_t, addr) {
                        Ok(a) => {
                            log::info!("printer attached on usb, out {:#04x} in {:#04x}", a.out, a.inp);
                            *lock.lock().unwrap() = Some(a);
                            cv.notify_all();
                        }
                        Err(e) => log::warn!("usb device {addr} is not our printer: {e}"),
                    },
                    Plug::Gone(dev) => {
                        let mut cur = lock.lock().unwrap();
                        if cur.map(|a| a.dev) == Some(dev) {
                            log::warn!("printer detached from usb");
                            *cur = None;
                            usb_host_device_close(client_addr as usb_host_client_handle_t, dev);
                        }
                    }
                }
            }
        });

        Ok(Usb { attached, pending: Mutex::new(VecDeque::new()) })
    }
}

enum Plug {
    New(u8),
    Gone(usb_device_handle_t),
}

unsafe impl Send for Plug {}

unsafe extern "C" fn on_client_event(msg: *const usb_host_client_event_msg_t, arg: *mut c_void) {
    let tx = &*(arg as *const Sender<Plug>);
    let msg = &*msg;
    let plug = if msg.event == usb_host_client_event_t_USB_HOST_CLIENT_EVENT_NEW_DEV {
        Plug::New(msg.__bindgen_anon_1.new_dev.address)
    } else {
        Plug::Gone(msg.__bindgen_anon_1.dev_gone.dev_hdl)
    };
    let _ = tx.send(plug);
}

fn spawn(name: &str, stack: usize, f: impl FnOnce() + Send + 'static) {
    std::thread::Builder::new()
        .name(name.into())
        .stack_size(stack)
        .spawn(f)
        .expect("thread");
}

/// Open the device at `addr`, find its printer interface and endpoints, and
/// claim the interface. Anything else on the bus is refused.
unsafe fn attach(client: usb_host_client_handle_t, addr: u8) -> Result<Attached, EspError> {
    let mut dev: usb_device_handle_t = ptr::null_mut();
    esp!(usb_host_device_open(client, addr, &mut dev))?;

    let mut cd: *const usb_config_desc_t = ptr::null();
    esp!(usb_host_get_active_config_descriptor(dev, &mut cd))?;
    // The total length at offset 2 covers every interface and endpoint
    // descriptor that follows, each led by its own length and type byte.
    let head = std::slice::from_raw_parts(cd as *const u8, 4);
    let total = u16::from_le_bytes([head[2], head[3]]) as usize;
    let c = std::slice::from_raw_parts(cd as *const u8, total);

    let (mut intf, mut out, mut inp) = (None, None, None);
    let mut in_printer = false;
    let mut i = 0;
    while i + 1 < c.len() && c[i] != 0 {
        match c[i + 1] as u32 {
            USB_B_DESCRIPTOR_TYPE_INTERFACE => {
                in_printer = c[i + 5] as u32 == USB_CLASS_PRINTER;
                if in_printer && intf.is_none() {
                    intf = Some(c[i + 2]);
                }
            }
            USB_B_DESCRIPTOR_TYPE_ENDPOINT if in_printer && c[i + 3] & 3 == 2 => {
                let ep = c[i + 2];
                if ep & 0x80 == 0 {
                    out.get_or_insert(ep);
                } else {
                    inp.get_or_insert(ep);
                }
            }
            _ => {}
        }
        i += c[i] as usize;
    }

    let (Some(intf), Some(out), Some(inp)) = (intf, out, inp) else {
        usb_host_device_close(client, dev);
        return Err(EspError::from_infallible::<ESP_ERR_NOT_SUPPORTED>());
    };

    esp!(usb_host_interface_claim(client, dev, intf, 0))?;

    Ok(Attached { dev, out, inp })
}

impl Usb {
    /// The attached printer, waiting up to ATTACH_WAIT for one to appear.
    fn printer(&self) -> Result<Attached, EspError> {
        let (lock, cv) = &*self.attached;
        let mut cur = lock.lock().unwrap();
        let deadline = Instant::now() + ATTACH_WAIT;

        while cur.is_none() {
            let Some(left) = deadline.checked_duration_since(Instant::now()) else {
                log::warn!("no printer on usb");
                return Err(EspError::from_infallible::<ESP_ERR_NOT_FOUND>());
            };
            cur = cv.wait_timeout(cur, left).unwrap().0;
        }

        Ok(cur.unwrap())
    }

    /// One bulk transfer, waited for. The completion arrives on the client
    /// thread; a channel brings the status here. `None` on a timeout with
    /// nothing moved, which for a read means the printer had nothing to say.
    unsafe fn transfer(&self, ep: u8, data: &mut [u8], timeout: Duration) -> Result<Option<usize>, EspError> {
        let p = self.printer()?;
        let out = ep & 0x80 == 0;

        let mut xfer: *mut usb_transfer_t = ptr::null_mut();
        esp!(usb_host_transfer_alloc(data.len(), 0, &mut xfer))?;
        let x = &mut *xfer;
        if out {
            ptr::copy_nonoverlapping(data.as_ptr(), x.data_buffer, data.len());
        }
        x.num_bytes = data.len() as _;
        x.device_handle = p.dev;
        x.bEndpointAddress = ep;
        x.timeout_ms = timeout.as_millis().max(1) as u32;

        let (tx, rx) = mpsc::channel::<(u32, i32)>();
        let tx = Box::into_raw(Box::new(tx));
        x.context = tx as *mut c_void;
        x.callback = Some(on_transfer);

        let submitted = esp!(usb_host_transfer_submit(xfer));
        let result = match submitted {
            Err(e) => Err(e),
            Ok(()) => match rx.recv_timeout(timeout) {
                Ok((status, n)) if status == usb_transfer_status_t_USB_TRANSFER_STATUS_COMPLETED => {
                    if !out {
                        ptr::copy_nonoverlapping(x.data_buffer, data.as_mut_ptr(), n as usize);
                    }
                    Ok(Some(n as usize))
                }
                Ok((status, _)) if status == usb_transfer_status_t_USB_TRANSFER_STATUS_TIMED_OUT => Ok(None),
                Ok((status, n)) => {
                    log::warn!("usb transfer on {ep:#04x}: status {status}, {n} bytes");
                    Err(EspError::from_infallible::<ESP_FAIL>())
                }
                Err(_) => {
                    // The library does not time transfers out on its own: a
                    // read the printer never answers stays in flight until
                    // told otherwise, and freeing it in that state is an
                    // assertion failure inside the host stack. Halting the
                    // endpoint and flushing it completes the transfer as
                    // cancelled, on the client thread, which the channel
                    // then reports; only after that may it be freed.
                    usb_host_endpoint_halt(p.dev, ep);
                    usb_host_endpoint_flush(p.dev, ep);
                    let _ = rx.recv_timeout(Duration::from_secs(2));
                    usb_host_endpoint_clear(p.dev, ep);
                    Ok(None)
                }
            },
        };

        drop(Box::from_raw(tx));
        usb_host_transfer_free(xfer);
        result
    }
}

unsafe extern "C" fn on_transfer(xfer: *mut usb_transfer_t) {
    let x = &*xfer;
    let tx = &*(x.context as *const Sender<(u32, i32)>);
    let _ = tx.send((x.status, x.actual_num_bytes));
}

impl Transport for Usb {
    /// Bulk-out in chunks. Each returns when the printer has taken the bytes;
    /// USB's own flow control holds a chunk while the printer's buffer is
    /// full, so a photo arrives exactly as fast as the printer can take it.
    fn write(&self, data: &[u8]) -> Result<(), EspError> {
        let p = self.printer()?;
        for chunk in data.chunks(CHUNK) {
            let mut buf = chunk.to_vec();
            match unsafe { self.transfer(p.out, &mut buf, Duration::from_secs(10)) }? {
                Some(n) if n == chunk.len() => {}
                Some(n) => {
                    log::warn!("printer took {n} of {} bytes", chunk.len());
                    return Err(EspError::from_infallible::<ESP_FAIL>());
                }
                None => {
                    log::warn!("printer did not take a chunk within 10 s");
                    return Err(EspError::from_infallible::<ESP_ERR_TIMEOUT>());
                }
            }
        }
        Ok(())
    }

    fn read_byte(&self, timeout: Duration) -> Result<Option<u8>, EspError> {
        if let Some(b) = self.pending.lock().unwrap().pop_front() {
            return Ok(Some(b));
        }

        let p = self.printer()?;
        let mut buf = [0u8; 64];
        match unsafe { self.transfer(p.inp, &mut buf, timeout) }? {
            Some(n) if n > 0 => {
                let mut pending = self.pending.lock().unwrap();
                pending.extend(&buf[1..n]);
                Ok(Some(buf[0]))
            }
            _ => Ok(None),
        }
    }

    fn discard_input(&self) -> Result<(), EspError> {
        self.pending.lock().unwrap().clear();
        // Whatever the printer had queued comes out on a short read.
        let p = self.printer()?;
        let mut buf = [0u8; 64];
        let _ = unsafe { self.transfer(p.inp, &mut buf, Duration::from_millis(20)) };
        Ok(())
    }

}
