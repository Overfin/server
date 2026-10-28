// Package creation marks how an account is being created, for the module
// contract's account gates. Only the core sets the mark.
package creation

import "context"

type adoptingKey struct{}

// Adopting marks ctx as creating an account by bringing an existing one
// over.
func Adopting(ctx context.Context) context.Context {
	return context.WithValue(ctx, adoptingKey{}, true)
}

// IsAdopting reports whether ctx was marked by Adopting.
func IsAdopting(ctx context.Context) bool {
	v, _ := ctx.Value(adoptingKey{}).(bool)
	return v
}

type adminKey struct{}

// Admin marks ctx as creating the account of one of the site's admins.
func Admin(ctx context.Context) context.Context {
	return context.WithValue(ctx, adminKey{}, true)
}

// IsAdmin reports whether ctx was marked by Admin.
func IsAdmin(ctx context.Context) bool {
	v, _ := ctx.Value(adminKey{}).(bool)
	return v
}

type admittedKey struct{}

// Admitted marks ctx as creating the account of someone Overfin's own
// sign-in already let in: an admin made them. Their invitation, if any,
// still shapes the account. Someone who registered isn't marked: their
// invitation is what lets them in.
func Admitted(ctx context.Context) context.Context {
	return context.WithValue(ctx, admittedKey{}, true)
}

type heldKey struct{}

// Held marks ctx as creating the account of someone who registered with
// Overfin's own sign-in: load reads the invitation they registered with
// (their hold on a use of it), kept on their record.
func Held(ctx context.Context, load func(context.Context) (string, error)) context.Context {
	return context.WithValue(ctx, heldKey{}, load)
}

// HeldBy is the function given to Held, nil when ctx wasn't marked.
func HeldBy(ctx context.Context) func(context.Context) (string, error) {
	load, _ := ctx.Value(heldKey{}).(func(context.Context) (string, error))
	return load
}

// IsAdmitted reports whether ctx was marked by Admitted.
func IsAdmitted(ctx context.Context) bool {
	v, _ := ctx.Value(admittedKey{}).(bool)
	return v
}
