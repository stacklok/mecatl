// Package history implements DTO-facing acceptance and reads for independent,
// linear append-only kernel histories keyed by generic resource IDs. The resource
// owner chooses its kind; History imposes no history-specific kind or second ID.
// History owns protocol and codec policy;
// Backend supplies atomic indexed byte storage, not execution. The format package
// defines mutable persistence DTOs whose owner-generated methods provide canonical
// encoding.
package history
