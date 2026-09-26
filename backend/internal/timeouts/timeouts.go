// Package timeouts is every wait on the print path, in the order they nest,
// so the chain reads and is tested in one place: the firmware's own deadline,
// then the dispatcher waiting for its ack, then the gateway waiting for the
// dispatcher, then the servers' write timeout around both.
//
// They are flat, not sized to the payload: over USB a photo prints in about
// a second, so the only slow job is one whose printer is missing or stalled,
// and the firmware bounds that itself.
package timeouts

import "time"

// Firmware is how long the firmware may take to ack a job: its JOB_DEADLINE
// (firmware/src/config.rs), plus the two seconds cancelling a transfer caught
// mid-flight can add. Change them together.
const Firmware = 20*time.Second + 2*time.Second

// Ack is how long the dispatcher waits for the firmware's answer: longer than
// the firmware's own limit, so a printer that gives up gets to say why rather
// than leaving the dispatcher to guess.
const Ack = Firmware + 3*time.Second

// Print is how long the gateway waits for the dispatcher: its Ack plus three
// seconds for the hops in between.
const Print = Ack + 3*time.Second

// Write is for the gateway's and dispatcher's HTTP servers. It must exceed
// Print, or a print legitimately waiting on the printer is cut off before it
// can answer.
const Write = 35 * time.Second
