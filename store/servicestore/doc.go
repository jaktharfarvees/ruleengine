// Package servicestore loads coherent snapshots from the Central Rule Service
// bootstrap and changes-since APIs. Its transport is lazy, HTTPS-only outside
// explicitly enabled loopback development, redirect-free, and bounded by
// attempt, time, and response size.
package servicestore
