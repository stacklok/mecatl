// Package history implements DTO-facing acceptance and reads for independent,
// linear append-only kernel histories. History owns protocol and codec policy;
// Backend supplies atomic indexed byte storage, not execution. The format package
// defines mutable persistence DTOs and canonical encoding.
package history
