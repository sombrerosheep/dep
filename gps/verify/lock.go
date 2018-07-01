// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package verify

import (
	"github.com/armon/go-radix"
	"github.com/golang/dep/gps"
	"github.com/golang/dep/gps/paths"
	"github.com/golang/dep/gps/pkgtree"
)

// VerifiableProject composes a LockedProject to indicate what the hash digest
// of a file tree for that LockedProject should be, given the PruneOptions and
// the list of packages.
type VerifiableProject struct {
	gps.LockedProject
	PruneOpts gps.PruneOptions
	Digest    VersionedDigest
}

// ConstraintMismatch is a two-tuple of a gps.Version, and a gps.Constraint that
// does not allow that version.
type ConstraintMismatch struct {
	C gps.Constraint
	V gps.Version
}

// LockSatisfaction holds the compound result of LockSatisfiesInputs, allowing
// the caller to inspect each of several orthogonal possible types of failure.
type LockSatisfaction struct {
	nolock                  bool
	missingPkgs, excessPkgs []string
	badovr, badconstraint   map[gps.ProjectRoot]ConstraintMismatch
}

// Passed is a shortcut method that indicates whether there were any ways in
// which the Lock did not satisfy the inputs. It will return true only if no
// problems were found.
func (ls LockSatisfaction) Passed() bool {
	if ls.nolock {
		return false
	}

	if len(ls.missingPkgs) > 0 {
		return false
	}

	if len(ls.excessPkgs) > 0 {
		return false
	}

	if len(ls.badovr) > 0 {
		return false
	}

	if len(ls.badconstraint) > 0 {
		return false
	}

	return true
}

// MissingImports reports the set of import paths that were present in the
// inputs but missing in the Lock.
func (ls LockSatisfaction) MissingImports() []string {
	return ls.missingPkgs
}

// ExcessImports reports the set of import paths that were present in the Lock
// but absent from the inputs.
func (ls LockSatisfaction) ExcessImports() []string {
	return ls.excessPkgs
}

// UnmatchedOverrides reports any override rules that were not satisfied by the
// corresponding LockedProject in the Lock.
func (ls LockSatisfaction) UnmatchedOverrides() map[gps.ProjectRoot]ConstraintMismatch {
	return ls.badovr
}

// UnmatchedOverrides reports any normal, non-override constraint rules that
// were not satisfied by the corresponding LockedProject in the Lock.
func (ls LockSatisfaction) UnmatchedConstraints() map[gps.ProjectRoot]ConstraintMismatch {
	return ls.badconstraint
}

func findEffectualConstraints(m gps.Manifest, imports map[string]bool) map[string]bool {
	eff := make(map[string]bool)
	xt := radix.New()

	for pr, _ := range m.DependencyConstraints() {
		// FIXME(sdboyer) this has the trailing slash ambiguity problem; adapt
		// code from the solver
		xt.Insert(string(pr), nil)
	}

	for imp := range imports {
		if root, _, has := xt.LongestPrefix(imp); has {
			eff[root] = true
		}
	}

	return eff
}

// LockSatisfiesInputs determines whether the provided Lock satisfies all the
// requirements indicated by the inputs (RootManifest and PackageTree).
//
// The second parameter is expected to be the list of imports that were used to
// generate the input Lock. Without this explicit list, it is not possible to
// compute package imports that may have been removed. Figuring out that
// negative space would require exploring the entire graph to ensure there are
// no in-edges for particular imports.
func LockSatisfiesInputs(l gps.LockWithImports, m gps.RootManifest, rpt pkgtree.PackageTree) LockSatisfaction {
	if l == nil {
		return LockSatisfaction{nolock: true}
	}

	var ig *pkgtree.IgnoredRuleset
	var req map[string]bool
	if m != nil {
		ig = m.IgnoredPackages()
		req = m.RequiredPackages()
	}

	rm, _ := rpt.ToReachMap(true, true, false, ig)
	reach := rm.FlattenFn(paths.IsStandardImportPath)

	inlock := make(map[string]bool, len(l.InputImports()))
	ininputs := make(map[string]bool, len(reach)+len(req))

	type lockUnsatisfy uint8
	const (
		missingFromLock lockUnsatisfy = iota
		inAdditionToLock
	)

	pkgDiff := make(map[string]lockUnsatisfy)

	for _, imp := range reach {
		ininputs[imp] = true
	}

	for imp := range req {
		ininputs[imp] = true
	}

	for _, imp := range l.InputImports() {
		inlock[imp] = true
	}

	lsat := LockSatisfaction{
		badovr:        make(map[gps.ProjectRoot]ConstraintMismatch),
		badconstraint: make(map[gps.ProjectRoot]ConstraintMismatch),
	}

	for ip := range ininputs {
		if !inlock[ip] {
			pkgDiff[ip] = missingFromLock
		} else {
			// So we don't have to revisit it below
			delete(inlock, ip)
		}
	}

	// Something in the missing list might already be in the packages list,
	// because another package in the depgraph imports it. We could make a
	// special case for that, but it would break the simplicity of the model and
	// complicate the notion of LockSatisfaction.Passed(), so let's see if we
	// can get away without it.

	for ip := range inlock {
		if !ininputs[ip] {
			pkgDiff[ip] = inAdditionToLock
		}
	}

	for ip, typ := range pkgDiff {
		if typ == missingFromLock {
			lsat.missingPkgs = append(lsat.missingPkgs, ip)
		} else {
			lsat.excessPkgs = append(lsat.excessPkgs, ip)
		}
	}

	eff := findEffectualConstraints(m, ininputs)
	ovr, constraints := m.Overrides(), m.DependencyConstraints()

	for _, lp := range l.Projects() {
		pr := lp.Ident().ProjectRoot

		if pp, has := ovr[pr]; has {
			if !pp.Constraint.Matches(lp.Version()) {
				lsat.badovr[pr] = ConstraintMismatch{
					C: pp.Constraint,
					V: lp.Version(),
				}
			}
			// The constraint isn't considered if we have an override,
			// independent of whether the override is satisfied.
			continue
		}

		if pp, has := constraints[pr]; has && eff[string(pr)] && !pp.Constraint.Matches(lp.Version()) {
			lsat.badconstraint[pr] = ConstraintMismatch{
				C: pp.Constraint,
				V: lp.Version(),
			}
		}
	}

	return lsat
}
