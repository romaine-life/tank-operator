// Package main — TEMPORARY marker to drive a live end-to-end check of the
// event-driven provisioning gate (stage 2): a fresh build publishes a
// sha-<commit> tag, the ACR push webhook records ci_image_available, and the
// gate provisions off that durable row (no GitHub polling). Safe to delete.
package main
