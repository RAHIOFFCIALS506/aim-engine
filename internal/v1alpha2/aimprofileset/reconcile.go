// MIT License
//
// Copyright (c) 2025 Advanced Micro Devices, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package aimprofileset

import (
	"context"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	aimv1alpha2 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha2"
	"github.com/amd-enterprise-ai/aim-engine/internal/constants"
	controllerutils "github.com/amd-enterprise-ai/aim-engine/internal/controller/utils"
	"github.com/amd-enterprise-ai/aim-engine/internal/utils"
	"github.com/amd-enterprise-ai/aim-engine/internal/v1alpha2/aimmodel"
	"github.com/amd-enterprise-ai/aim-engine/internal/v1alpha2/aimprofile"
	"github.com/amd-enterprise-ai/aim-engine/internal/v1alpha2/profileyaml"
)

const (
	annotationProfileSetUID       = constants.AimLabelDomain + "/profile-set-uid"
	annotationProfileSetName      = constants.AimLabelDomain + "/profile-set-name"
	annotationProfileSetNamespace = constants.AimLabelDomain + "/profile-set-namespace"
	ManagedProfileSetUIDIndexKey  = ".metadata.annotations." + annotationProfileSetUID

	componentDerivation = "Derivation"
)

func AnnotationProfileSetName() string {
	return annotationProfileSetName
}

func AnnotationProfileSetUID() string {
	return annotationProfileSetUID
}

func AnnotationProfileSetNamespace() string {
	return annotationProfileSetNamespace
}

type ProfileSetReconciler struct {
	Scheme *runtime.Scheme
	// APIReader is the uncached reader used to load a sourceRef catalog directly
	// from the API server.
	APIReader client.Reader
}

type ClusterProfileSetReconciler struct {
	Scheme *runtime.Scheme
	// APIReader is the uncached reader used to load a sourceRef catalog directly
	// from the API server.
	APIReader client.Reader
}

// catalogReader returns the reader used to load a sourceRef catalog ConfigMap.
// Falls back to the cached client when APIReader is unset (unit tests).
func catalogReader(apiReader client.Reader, cached client.Client) client.Reader {
	if apiReader != nil {
		return apiReader
	}
	return cached
}

type managedProfile struct {
	Object client.Object
	Status aimv1alpha2.AIMProfileStatus
}

type desiredProfile struct {
	Object client.Object
}

// profileSetCandidate keeps the profile resource metadata needed to resolve
// its YAML contract after selector and version-policy filtering. Catalog
// candidates already carry an inspected contract and are marked resolved.
type profileSetCandidate struct {
	aimprofile.ProfileCopyCandidate
	annotations      map[string]string
	labels           map[string]string
	sourceKind       string
	contractResolved bool
}

type ProfileSetFetchResult struct {
	set        *aimv1alpha2.AIMProfileSet
	candidates controllerutils.FetchResult[[]profileSetCandidate]
	managed    controllerutils.FetchResult[[]managedProfile]
}

type ClusterProfileSetFetchResult struct {
	set        *aimv1alpha2.AIMClusterProfileSet
	candidates controllerutils.FetchResult[[]profileSetCandidate]
	managed    controllerutils.FetchResult[[]managedProfile]
}

type ProfileSetObservation struct {
	desiredProfiles      []desiredProfile
	existingManaged      []managedProfile
	managedProfiles      aimv1alpha1.ManagedProfileCounts
	existingDesiredCount int32
	pruneSafe            bool
	buildErr             error
	componentHealth      []controllerutils.ComponentHealth
}

func (obs ProfileSetObservation) GetComponentHealth(_ context.Context, _ kubernetes.Interface) []controllerutils.ComponentHealth {
	return obs.componentHealth
}

type ClusterProfileSetObservation struct {
	desiredProfiles      []desiredProfile
	existingManaged      []managedProfile
	managedProfiles      aimv1alpha1.ManagedProfileCounts
	existingDesiredCount int32
	pruneSafe            bool
	buildErr             error
	componentHealth      []controllerutils.ComponentHealth
}

func (obs ClusterProfileSetObservation) GetComponentHealth(_ context.Context, _ kubernetes.Interface) []controllerutils.ComponentHealth {
	return obs.componentHealth
}

func (r *ProfileSetReconciler) FetchRemoteState(
	ctx context.Context,
	c client.Client,
	reconcileCtx controllerutils.ReconcileContext[*aimv1alpha2.AIMProfileSet],
) ProfileSetFetchResult {
	set := reconcileCtx.Object
	var candidates []profileSetCandidate
	var candidateErr error
	if validateProfileSetSourceScope(set.Spec) == nil {
		candidates, candidateErr = loadNamespaceCandidates(ctx, c, catalogReader(r.APIReader, c), set)
	}
	managed, managedErr := listManagedNamespaceProfiles(ctx, c, set.Namespace, string(set.UID))
	return ProfileSetFetchResult{
		set:        set,
		candidates: controllerutils.FetchResult[[]profileSetCandidate]{Value: candidates, Error: candidateErr},
		managed:    controllerutils.FetchResult[[]managedProfile]{Value: managed, Error: managedErr},
	}
}

func (r *ClusterProfileSetReconciler) FetchRemoteState(
	ctx context.Context,
	c client.Client,
	reconcileCtx controllerutils.ReconcileContext[*aimv1alpha2.AIMClusterProfileSet],
) ClusterProfileSetFetchResult {
	set := reconcileCtx.Object
	var candidates []profileSetCandidate
	var candidateErr error
	if validateProfileSetSourceScope(set.Spec) == nil {
		candidates, candidateErr = loadClusterCandidates(ctx, c, catalogReader(r.APIReader, c), set)
	}
	managed, managedErr := listManagedClusterProfiles(ctx, c, string(set.UID))
	return ClusterProfileSetFetchResult{
		set:        set,
		candidates: controllerutils.FetchResult[[]profileSetCandidate]{Value: candidates, Error: candidateErr},
		managed:    controllerutils.FetchResult[[]managedProfile]{Value: managed, Error: managedErr},
	}
}

func (r *ProfileSetReconciler) ComposeState(
	_ context.Context,
	_ controllerutils.ReconcileContext[*aimv1alpha2.AIMProfileSet],
	fetch ProfileSetFetchResult,
) ProfileSetObservation {
	obs := ProfileSetObservation{}
	obs.buildErr = validateProfileSetSourceScope(fetch.set.Spec)
	if obs.buildErr == nil && fetch.candidates.Error == nil {
		var selected []aimprofile.ProfileCopyCandidate
		selected, obs.buildErr = resolveSelectedProfileSetCandidates(fetch.candidates.Value, profileCopyRequest(fetch.set.Spec))
		if obs.buildErr == nil {
			obs.desiredProfiles, obs.buildErr = buildDesiredNamespaceProfiles(fetch.set, selected)
		}
		if obs.buildErr == nil && fetch.managed.Error == nil {
			obs.buildErr = stabilizeDerivedProfileNames(obs.desiredProfiles, fetch.managed.Value)
		}
	}
	if fetch.managed.Error == nil {
		obs.existingManaged = fetch.managed.Value
		obs.managedProfiles, obs.existingDesiredCount = summarizeManagedProfiles(obs.desiredProfiles, fetch.managed.Value)
	}
	obs.pruneSafe = fetch.candidates.Error == nil &&
		fetch.managed.Error == nil &&
		obs.buildErr == nil &&
		profileSetPruneReady(obs.desiredProfiles, obs.existingManaged)
	obs.componentHealth = buildComponentHealth(collectErrors(fetch.candidates.Error, fetch.managed.Error), obs.buildErr, obs.managedProfiles, obs.existingDesiredCount)
	return obs
}

func (r *ClusterProfileSetReconciler) ComposeState(
	_ context.Context,
	_ controllerutils.ReconcileContext[*aimv1alpha2.AIMClusterProfileSet],
	fetch ClusterProfileSetFetchResult,
) ClusterProfileSetObservation {
	obs := ClusterProfileSetObservation{}
	obs.buildErr = validateProfileSetSourceScope(fetch.set.Spec)
	if obs.buildErr == nil && fetch.candidates.Error == nil {
		var selected []aimprofile.ProfileCopyCandidate
		selected, obs.buildErr = resolveSelectedProfileSetCandidates(fetch.candidates.Value, profileCopyRequest(fetch.set.Spec))
		if obs.buildErr == nil {
			obs.desiredProfiles, obs.buildErr = buildDesiredClusterProfiles(fetch.set, selected)
		}
		if obs.buildErr == nil && fetch.managed.Error == nil {
			obs.buildErr = stabilizeDerivedProfileNames(obs.desiredProfiles, fetch.managed.Value)
		}
	}
	if fetch.managed.Error == nil {
		obs.existingManaged = fetch.managed.Value
		obs.managedProfiles, obs.existingDesiredCount = summarizeManagedProfiles(obs.desiredProfiles, fetch.managed.Value)
	}
	obs.pruneSafe = fetch.candidates.Error == nil &&
		fetch.managed.Error == nil &&
		obs.buildErr == nil &&
		profileSetPruneReady(obs.desiredProfiles, obs.existingManaged)
	obs.componentHealth = buildComponentHealth(collectErrors(fetch.candidates.Error, fetch.managed.Error), obs.buildErr, obs.managedProfiles, obs.existingDesiredCount)
	return obs
}

func (r *ProfileSetReconciler) PlanResources(
	_ context.Context,
	_ controllerutils.ReconcileContext[*aimv1alpha2.AIMProfileSet],
	obs ProfileSetObservation,
) controllerutils.PlanResult {
	var plan controllerutils.PlanResult
	if obs.buildErr != nil {
		return plan
	}
	for _, desired := range obs.desiredProfiles {
		plan.Apply(desired.Object)
	}
	if !obs.pruneSafe {
		return plan
	}
	desiredNames := desiredProfileNames(obs.desiredProfiles)
	for _, existingProfile := range obs.existingManaged {
		if _, keep := desiredNames[client.ObjectKeyFromObject(existingProfile.Object).String()]; !keep {
			plan.Delete(existingProfile.Object)
		}
	}
	return plan
}

func (r *ClusterProfileSetReconciler) PlanResources(
	_ context.Context,
	_ controllerutils.ReconcileContext[*aimv1alpha2.AIMClusterProfileSet],
	obs ClusterProfileSetObservation,
) controllerutils.PlanResult {
	var plan controllerutils.PlanResult
	if obs.buildErr != nil {
		return plan
	}
	for _, desired := range obs.desiredProfiles {
		plan.Apply(desired.Object)
	}
	if !obs.pruneSafe {
		return plan
	}
	desiredNames := desiredProfileNames(obs.desiredProfiles)
	for _, existingProfile := range obs.existingManaged {
		if _, keep := desiredNames[client.ObjectKeyFromObject(existingProfile.Object).String()]; !keep {
			plan.Delete(existingProfile.Object)
		}
	}
	return plan
}

func (r *ProfileSetReconciler) DecorateStatus(
	status *aimv1alpha1.AIMProfileSetStatus,
	_ *controllerutils.ConditionManager,
	obs ProfileSetObservation,
) {
	status.ManagedProfiles = obs.managedProfiles
}

func (r *ClusterProfileSetReconciler) DecorateStatus(
	status *aimv1alpha1.AIMProfileSetStatus,
	_ *controllerutils.ConditionManager,
	obs ClusterProfileSetObservation,
) {
	status.ManagedProfiles = obs.managedProfiles
}

func buildDesiredNamespaceProfiles(set *aimv1alpha2.AIMProfileSet, candidates []aimprofile.ProfileCopyCandidate) ([]desiredProfile, error) {
	req := profileCopyRequest(set.Spec)
	matched, err := aimprofile.FilterProfileCopyCandidates(candidates, req)
	if err != nil {
		return nil, controllerutils.NewInvalidSpecError("InvalidProfileSelection", err.Error(), err)
	}
	desired := make([]desiredProfile, 0, len(matched))
	for _, source := range matched {
		name, err := utils.GenerateDerivedName(
			[]string{set.Name, firstNonEmpty(source.Spec.ProfileId, source.Name)},
			utils.WithHashSource(set.Name, candidateIdentity(source)),
		)
		if err != nil {
			return nil, fmt.Errorf("generate derived profile name: %w", err)
		}
		copiedSpec, err := aimprofile.ApplyProfileCopyOverrides(source.Spec, set.Spec.Overrides, set.Spec.Image, source.BaseImage)
		if err != nil {
			return nil, err
		}
		profile := &aimv1alpha2.AIMProfile{}
		profile.Namespace = set.Namespace
		profile.Name = name
		profile.Annotations = aimprofile.MarkProfileSource(map[string]string{
			annotationProfileSetUID:       string(set.UID),
			annotationProfileSetName:      set.Name,
			annotationProfileSetNamespace: set.Namespace,
		}, aimprofile.ProfileSourceCopy)
		profile.Annotations = profileyaml.Mark(profile.Annotations, source.YAMLContract)
		profile.Spec = aimv1alpha2.AIMProfileSpec{AIMProfileSpecCommon: copiedSpec}
		profile.Spec.ImagePullSecrets = inheritedPullSecrets(set.Spec, source.Spec)
		profile.Spec.ServiceAccountName = inheritedServiceAccount(set.Spec, source.Spec)
		aimprofile.StampProfileProvenance(
			profile,
			aimprofile.ProfileRoleLabelValue(copiedSpec),
			aimv1alpha1.ProfileOriginDerived,
			namespaceProfileSetSourceModel(set),
		)
		desired = append(desired, desiredProfile{Object: profile})
	}
	return desired, nil
}

func buildDesiredClusterProfiles(set *aimv1alpha2.AIMClusterProfileSet, candidates []aimprofile.ProfileCopyCandidate) ([]desiredProfile, error) {
	req := profileCopyRequest(set.Spec)
	matched, err := aimprofile.FilterProfileCopyCandidates(candidates, req)
	if err != nil {
		return nil, controllerutils.NewInvalidSpecError("InvalidProfileSelection", err.Error(), err)
	}
	desired := make([]desiredProfile, 0, len(matched))
	for _, source := range matched {
		name, err := utils.GenerateDerivedName(
			[]string{set.Name, firstNonEmpty(source.Spec.ProfileId, source.Name)},
			utils.WithHashSource(set.Name, candidateIdentity(source)),
		)
		if err != nil {
			return nil, fmt.Errorf("generate derived profile name: %w", err)
		}
		copiedSpec, err := aimprofile.ApplyProfileCopyOverrides(source.Spec, set.Spec.Overrides, set.Spec.Image, source.BaseImage)
		if err != nil {
			return nil, err
		}
		profile := &aimv1alpha2.AIMClusterProfile{}
		profile.Name = name
		profile.Annotations = aimprofile.MarkProfileSource(map[string]string{
			annotationProfileSetUID:  string(set.UID),
			annotationProfileSetName: set.Name,
		}, aimprofile.ProfileSourceCopy)
		profile.Annotations = profileyaml.Mark(profile.Annotations, source.YAMLContract)
		profile.Spec = aimv1alpha2.AIMClusterProfileSpec{AIMProfileSpecCommon: copiedSpec}
		profile.Spec.ImagePullSecrets = inheritedPullSecrets(set.Spec, source.Spec)
		profile.Spec.ServiceAccountName = inheritedServiceAccount(set.Spec, source.Spec)
		aimprofile.StampProfileProvenance(
			profile,
			aimprofile.ProfileRoleLabelValue(copiedSpec),
			aimv1alpha1.ProfileOriginDerived,
			clusterProfileSetSourceModel(set),
		)
		desired = append(desired, desiredProfile{Object: profile})
	}
	return desired, nil
}

// namespaceProfileSetSourceModel returns the target-model identity propagated
// onto a model-owned child profile set. A standalone set's sourceRef identifies
// input, not ownership, so it deliberately does not stamp that source model
// onto its outputs.
func namespaceProfileSetSourceModel(set *aimv1alpha2.AIMProfileSet) *aimv1alpha2.ProfileSourceModel {
	return sourceModelFromProfileSetLabels(set.Labels, set.Namespace)
}

func clusterProfileSetSourceModel(set *aimv1alpha2.AIMClusterProfileSet) *aimv1alpha2.ProfileSourceModel {
	return sourceModelFromProfileSetLabels(set.Labels, "")
}

func validateProfileSetSourceScope(spec aimv1alpha1.AIMProfileSetSpec) error {
	if spec.SourceRef == nil || spec.Selector.ModelRef == nil {
		return nil
	}
	return controllerutils.NewInvalidSpecError(
		"ConflictingProfileSources",
		"spec.sourceRef cannot be combined with spec.selector.modelRef; sourceRef selects a catalog while modelRef filters already-materialized profiles",
		nil,
	)
}

func sourceModelFromProfileSetLabels(setLabels map[string]string, namespace string) *aimv1alpha2.ProfileSourceModel {
	name := setLabels[constants.LabelKeySourceModel]
	if name == "" {
		return nil
	}
	if setLabels[constants.LabelKeySourceModelScope] == constants.LabelValueSourceModelScopeCluster {
		return &aimv1alpha2.ProfileSourceModel{
			Name: name,
			Kind: aimv1alpha2.ProfileSourceModelKindAIMClusterModel,
		}
	}
	return &aimv1alpha2.ProfileSourceModel{
		Name:      name,
		Kind:      aimv1alpha2.ProfileSourceModelKindAIMModel,
		Namespace: namespace,
	}
}

func profileCopyRequest(spec aimv1alpha1.AIMProfileSetSpec) aimprofile.ProfileCopyRequest {
	return aimprofile.ProfileCopyRequest{
		Selector:      spec.Selector,
		VersionPolicy: normalizedVersionPolicy(spec.VersionPolicy),
		Version:       spec.Version,
		Overrides:     spec.Overrides,
		ImageOverride: spec.Image,
	}
}

func resolveSelectedProfileSetCandidates(
	candidates []profileSetCandidate,
	req aimprofile.ProfileCopyRequest,
) ([]aimprofile.ProfileCopyCandidate, error) {
	provenance, _, err := aimprofile.ProvenanceLabelSelector(req.Selector)
	if err != nil {
		return nil, controllerutils.NewInvalidSpecError("InvalidProfileSelection", err.Error(), err)
	}

	eligible := make([]profileSetCandidate, 0, len(candidates))
	for i := range candidates {
		if provenance == nil || provenance.Matches(labels.Set(candidates[i].labels)) {
			eligible = append(eligible, candidates[i])
		}
	}
	plain := make([]aimprofile.ProfileCopyCandidate, len(eligible))
	for i := range eligible {
		plain[i] = eligible[i].ProfileCopyCandidate
	}

	selectedIndexes, err := aimprofile.FilterProfileCopyCandidateIndexes(plain, req)
	if err != nil {
		return nil, controllerutils.NewInvalidSpecError("InvalidProfileSelection", err.Error(), err)
	}

	resolved := make([]aimprofile.ProfileCopyCandidate, 0, len(selectedIndexes))
	for _, index := range selectedIndexes {
		candidate, err := eligible[index].resolved()
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, candidate)
	}
	return resolved, nil
}

func (candidate profileSetCandidate) resolved() (aimprofile.ProfileCopyCandidate, error) {
	if candidate.contractResolved {
		return candidate.ProfileCopyCandidate, nil
	}

	spec := candidate.Spec
	contract, err := profileyaml.ForProfile(
		candidate.annotations,
		profileyaml.EffectiveOrigin(candidate.labels, candidate.Status.Origin),
		&spec,
	)
	if err != nil {
		return aimprofile.ProfileCopyCandidate{}, fmt.Errorf(
			"parse profile YAML contract on %s %q: %w",
			candidate.sourceKind,
			candidate.Name,
			err,
		)
	}
	candidate.YAMLContract = contract
	return candidate.ProfileCopyCandidate, nil
}

func normalizedVersionPolicy(policy aimv1alpha1.ProfileVersionPolicy) aimv1alpha1.ProfileVersionPolicy {
	if policy == "" {
		return aimv1alpha1.ProfileVersionPolicyPinned
	}
	return policy
}

func inheritedPullSecrets(spec aimv1alpha1.AIMProfileSetSpec, source aimv1alpha2.AIMProfileSpecCommon) []corev1.LocalObjectReference {
	if len(spec.ImagePullSecrets) > 0 {
		out := make([]corev1.LocalObjectReference, len(spec.ImagePullSecrets))
		copy(out, spec.ImagePullSecrets)
		return out
	}
	out := make([]corev1.LocalObjectReference, len(source.ImagePullSecrets))
	copy(out, source.ImagePullSecrets)
	return out
}

func inheritedServiceAccount(spec aimv1alpha1.AIMProfileSetSpec, source aimv1alpha2.AIMProfileSpecCommon) string {
	if spec.ServiceAccountName != "" {
		return spec.ServiceAccountName
	}
	return source.ServiceAccountName
}

func summarizeManagedProfiles(desired []desiredProfile, existing []managedProfile) (aimv1alpha1.ManagedProfileCounts, int32) {
	result := aimv1alpha1.ManagedProfileCounts{Total: int32(len(desired))}
	for _, d := range desired {
		if spec, ok := profileSpecCommon(d.Object); ok && aimprofile.IsProfileDeployable(spec) {
			result.Deployable++
		} else {
			result.Base++
		}
	}
	if len(desired) == 0 || len(existing) == 0 {
		return result, 0
	}
	existingByKey := make(map[string]managedProfile, len(existing))
	for _, profile := range existing {
		existingByKey[client.ObjectKeyFromObject(profile.Object).String()] = profile
	}
	var existingDesiredCount int32
	for _, desired := range desired {
		key := client.ObjectKeyFromObject(desired.Object).String()
		profile, found := existingByKey[key]
		if !found {
			continue
		}
		wantedSpec, wantedOK := profileSpecCommon(desired.Object)
		currentSpec, currentOK := profileSpecCommon(profile.Object)
		if !wantedOK || !currentOK || !aimprofile.ProfileSpecsEquivalent(currentSpec, wantedSpec) {
			continue
		}
		existingDesiredCount++
		if profile.Object.GetGeneration() > 0 &&
			profile.Status.ObservedGeneration != profile.Object.GetGeneration() {
			continue
		}
		switch profile.Status.Status {
		case constants.AIMStatusReady:
			result.Ready++
		case constants.AIMStatusNotAvailable:
			result.NotAvailable++
		}
	}
	return result, existingDesiredCount
}

// profileSpecCommon extracts the AIMProfileSpecCommon embedded in either an
// AIMProfile or AIMClusterProfile. The boolean is false for unrecognised
// kinds; counting code treats those as base profiles.
func profileSpecCommon(obj client.Object) (aimv1alpha2.AIMProfileSpecCommon, bool) {
	switch p := obj.(type) {
	case *aimv1alpha2.AIMProfile:
		return p.Spec.AIMProfileSpecCommon, true
	case *aimv1alpha2.AIMClusterProfile:
		return p.Spec.AIMProfileSpecCommon, true
	default:
		return aimv1alpha2.AIMProfileSpecCommon{}, false
	}
}

// stabilizeDerivedProfileNames preserves the historical name for ordinary
// derivations, but rolls to a deterministic alternate when that name is
// occupied by an owned profile whose non-empty aimId cannot be updated in
// place. Once the alternate exists for the requested identity, it remains the
// desired name so the controller does not oscillate back after pruning the old
// profile.
func stabilizeDerivedProfileNames(desired []desiredProfile, existing []managedProfile) error {
	if len(desired) == 0 || len(existing) == 0 {
		return nil
	}

	existingByKey := make(map[string]managedProfile, len(existing))
	for _, profile := range existing {
		existingByKey[client.ObjectKeyFromObject(profile.Object).String()] = profile
	}

	for i := range desired {
		wantedSpec, ok := profileSpecCommon(desired[i].Object)
		if !ok {
			return fmt.Errorf("cannot resolve immutable identity for unsupported desired profile type %T", desired[i].Object)
		}
		baseKey := client.ObjectKeyFromObject(desired[i].Object)
		alternateName, err := utils.GenerateDerivedName(
			[]string{baseKey.Name},
			utils.WithHashSource(baseKey.Name, "aimId", wantedSpec.AimId),
		)
		if err != nil {
			return fmt.Errorf("generate identity-safe derived profile name: %w", err)
		}
		alternateKey := client.ObjectKey{Namespace: baseKey.Namespace, Name: alternateName}

		if alternate, found := existingByKey[alternateKey.String()]; found {
			alternateSpec, recognized := profileSpecCommon(alternate.Object)
			if !recognized {
				return fmt.Errorf("cannot inspect identity-safe profile %s of type %T", alternateKey, alternate.Object)
			}
			if alternateSpec.AimId != "" && alternateSpec.AimId != wantedSpec.AimId {
				return fmt.Errorf(
					"identity-safe profile name %s is occupied by immutable aimId %q instead of %q",
					alternateKey,
					alternateSpec.AimId,
					wantedSpec.AimId,
				)
			}
			desired[i].Object.SetName(alternateName)
			continue
		}

		current, found := existingByKey[baseKey.String()]
		if !found {
			continue
		}
		currentSpec, recognized := profileSpecCommon(current.Object)
		if !recognized {
			return fmt.Errorf("cannot inspect existing profile %s of type %T", baseKey, current.Object)
		}
		if currentSpec.AimId != "" && currentSpec.AimId != wantedSpec.AimId {
			desired[i].Object.SetName(alternateName)
		}
	}
	return nil
}

func buildComponentHealth(fetchErrs []error, buildErr error, managed aimv1alpha1.ManagedProfileCounts, existingDesiredCount int32) []controllerutils.ComponentHealth {
	if len(fetchErrs) > 0 {
		return []controllerutils.ComponentHealth{{
			Component:      componentDerivation,
			State:          constants.AIMStatusDegraded,
			Reason:         "SourceLoadFailed",
			Message:        errors.Join(fetchErrs...).Error(),
			Errors:         fetchErrs,
			DependencyType: controllerutils.DependencyTypeUpstream,
		}}
	}
	if buildErr != nil {
		return []controllerutils.ComponentHealth{{
			Component:      componentDerivation,
			State:          constants.AIMStatusDegraded,
			Reason:         "DerivationFailed",
			Message:        buildErr.Error(),
			Errors:         []error{buildErr},
			DependencyType: controllerutils.DependencyTypeUpstream,
		}}
	}
	if managed.Total == 0 {
		return []controllerutils.ComponentHealth{{
			Component: componentDerivation,
			State:     constants.AIMStatusNotAvailable,
			Reason:    "NoMatchingProfiles",
			Message:   "No matching source profiles found for derivation",
		}}
	}
	if existingDesiredCount < managed.Total {
		return []controllerutils.ComponentHealth{{
			Component:      componentDerivation,
			State:          constants.AIMStatusProgressing,
			Reason:         constants.ReasonCreating,
			Message:        "Derived profiles are still being created",
			DependencyType: controllerutils.DependencyTypeDownstream,
		}}
	}
	if managed.Ready == managed.Total {
		return []controllerutils.ComponentHealth{{
			Component: componentDerivation,
			State:     constants.AIMStatusReady,
			Reason:    "ManagedProfilesReady",
		}}
	}
	if managed.Ready > 0 {
		return []controllerutils.ComponentHealth{{
			Component: componentDerivation,
			State:     constants.AIMStatusDegraded,
			Reason:    "ManagedProfilesPartiallyReady",
			Message:   "Some derived profiles are ready, but others are not available",
		}}
	}
	return []controllerutils.ComponentHealth{{
		Component: componentDerivation,
		State:     constants.AIMStatusNotAvailable,
		Reason:    "ManagedProfilesNotAvailable",
		Message:   "Derived profiles exist but none are ready",
	}}
}

func loadNamespaceCandidates(ctx context.Context, c client.Client, reader client.Reader, set *aimv1alpha2.AIMProfileSet) ([]profileSetCandidate, error) {
	if set.Spec.SourceRef != nil {
		switch set.Spec.SourceRef.EffectiveKind() {
		case aimv1alpha1.ProfileSourceKindConfigMap:
			_, catalog, err := aimprofile.LoadDiscoveryCatalog(ctx, reader, *set.Spec.SourceRef, set.Namespace)
			if err != nil {
				return nil, err
			}
			return resolvedProfileSetCandidates(catalog.ToCandidates()), nil
		case aimv1alpha1.ProfileSourceKindAIMModel:
			var model aimv1alpha2.AIMModel
			if err := reader.Get(ctx, client.ObjectKey{Name: set.Spec.SourceRef.Name, Namespace: set.Namespace}, &model); err != nil {
				return nil, fmt.Errorf("load source AIMModel %s/%s: %w", set.Namespace, set.Spec.SourceRef.Name, err)
			}
			return loadModelCatalogCandidates(ctx, reader, &model.Spec, &model.Status, model.Generation, string(model.UID), set.Namespace)
		case aimv1alpha1.ProfileSourceKindAIMClusterModel:
			var model aimv1alpha2.AIMClusterModel
			if err := reader.Get(ctx, client.ObjectKey{Name: set.Spec.SourceRef.Name}, &model); err != nil {
				return nil, fmt.Errorf("load source AIMClusterModel %q: %w", set.Spec.SourceRef.Name, err)
			}
			return loadModelCatalogCandidates(
				ctx,
				reader,
				&model.Spec,
				&model.Status,
				model.Generation,
				string(model.UID),
				constants.GetOperatorNamespace(),
			)
		default:
			return nil, fmt.Errorf("unsupported profile source kind %q", set.Spec.SourceRef.Kind)
		}
	}
	provenance, scope, err := aimprofile.ProvenanceLabelSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	// selector.aimId is always a strict source-side filter now. CEL on
	// AIMProfileSetSpec / AIMModelProfilesSpec rejects selector.aimId
	// when selector.role=base, so base-role selections never reach this
	// path with a non-empty aimId — target identity for derived profiles
	// is supplied via overrides.aimId (see ApplyProfileCopyOverrides).
	aimIDFilter := set.Spec.Selector.AimId
	// Selector.modelRef.scope==Cluster skips namespace AIMProfiles entirely.
	if scope == aimprofile.SelectorScopeCluster {
		return listClusterProfileCandidatesWithLabels(ctx, c, aimIDFilter, provenance)
	}
	namespaceCandidates, err := listNamespaceProfileCandidatesWithLabels(ctx, c, set.Namespace, aimIDFilter, provenance)
	if err != nil {
		return nil, err
	}
	// Selector.modelRef.scope==Namespace already restricts to namespace
	// profiles via the source-model-scope label, so no cluster fallback is
	// needed there. SelectorScopeAny (the default) falls back to cluster
	// profiles when no namespace match was found.
	if scope == aimprofile.SelectorScopeNamespace || len(namespaceCandidates) > 0 {
		return namespaceCandidates, nil
	}
	return listClusterProfileCandidatesWithLabels(ctx, c, aimIDFilter, provenance)
}

func loadClusterCandidates(ctx context.Context, c client.Client, reader client.Reader, set *aimv1alpha2.AIMClusterProfileSet) ([]profileSetCandidate, error) {
	if set.Spec.SourceRef != nil {
		switch set.Spec.SourceRef.EffectiveKind() {
		case aimv1alpha1.ProfileSourceKindConfigMap:
			namespace := constants.GetOperatorNamespace()
			_, catalog, err := aimprofile.LoadDiscoveryCatalog(ctx, reader, *set.Spec.SourceRef, namespace)
			if err != nil {
				return nil, fmt.Errorf("load sourceRef %q from operator namespace %q: %w", set.Spec.SourceRef.Name, namespace, err)
			}
			return resolvedProfileSetCandidates(catalog.ToCandidates()), nil
		case aimv1alpha1.ProfileSourceKindAIMClusterModel:
			var model aimv1alpha2.AIMClusterModel
			if err := reader.Get(ctx, client.ObjectKey{Name: set.Spec.SourceRef.Name}, &model); err != nil {
				return nil, fmt.Errorf("load source AIMClusterModel %q: %w", set.Spec.SourceRef.Name, err)
			}
			return loadModelCatalogCandidates(
				ctx,
				reader,
				&model.Spec,
				&model.Status,
				model.Generation,
				string(model.UID),
				constants.GetOperatorNamespace(),
			)
		case aimv1alpha1.ProfileSourceKindAIMModel:
			return nil, fmt.Errorf("AIMClusterProfileSet cannot use namespace-scoped AIMModel %q as a source", set.Spec.SourceRef.Name)
		default:
			return nil, fmt.Errorf("unsupported profile source kind %q", set.Spec.SourceRef.Kind)
		}
	}
	provenance, _, err := aimprofile.ProvenanceLabelSelector(set.Spec.Selector)
	if err != nil {
		return nil, err
	}
	// See loadNamespaceCandidates: Base-role selectors target aimId-less
	// base profiles; the index filter on selector.AimId would exclude them.
	aimIDFilter := set.Spec.Selector.AimId
	if set.Spec.Selector.Role == aimv1alpha1.ProfileSelectorRoleBase {
		aimIDFilter = ""
	}
	return listClusterProfileCandidatesWithLabels(ctx, c, aimIDFilter, provenance)
}

func loadModelCatalogCandidates(
	ctx context.Context,
	reader client.Reader,
	spec *aimv1alpha1.AIMModelSpec,
	status *aimv1alpha1.AIMModelStatus,
	generation int64,
	modelUID string,
	expectedCacheNamespace string,
) ([]profileSetCandidate, error) {
	if spec == nil || spec.Image == "" {
		return nil, fmt.Errorf("referenced model is not image-backed")
	}
	if status == nil {
		return nil, fmt.Errorf("referenced model has no discovery status")
	}
	if generation > 0 && status.ObservedGeneration != generation {
		return nil, fmt.Errorf(
			"referenced model discovery is stale: observed generation %d, current generation %d",
			status.ObservedGeneration,
			generation,
		)
	}
	if status.DiscoveryCacheRef == nil || status.DiscoveryCacheRef.Name == "" {
		return nil, fmt.Errorf("referenced model has not published a discovery cache")
	}
	cacheNamespace := status.DiscoveryCacheRef.Namespace
	if cacheNamespace == "" {
		cacheNamespace = expectedCacheNamespace
	}
	if cacheNamespace != expectedCacheNamespace {
		return nil, fmt.Errorf(
			"referenced model discovery cache namespace %q does not match expected namespace %q",
			cacheNamespace,
			expectedCacheNamespace,
		)
	}
	cache, catalog, err := aimprofile.LoadDiscoveryCatalog(
		ctx,
		reader,
		aimv1alpha1.ProfileSourceRef{
			Kind: aimv1alpha1.ProfileSourceKindConfigMap,
			Name: status.DiscoveryCacheRef.Name,
		},
		cacheNamespace,
	)
	if err != nil {
		return nil, fmt.Errorf("load referenced model discovery cache %s/%s: %w", cacheNamespace, status.DiscoveryCacheRef.Name, err)
	}
	if modelUID != "" && !aimmodel.CacheOwnedByModel(cache, modelUID) {
		return nil, fmt.Errorf("referenced discovery cache %s/%s is not owned by the current model", cacheNamespace, cache.Name)
	}
	expectedSpecHash := aimmodel.ComputeDiscoverySpecHash(
		spec.Image,
		spec.ImagePullSecrets,
		spec.ServiceAccountName,
		aimmodel.DiscoveryCommandVersion,
	)
	if !aimmodel.CacheHasMatchingSpecHash(cache, expectedSpecHash) {
		return nil, fmt.Errorf("referenced model discovery cache %s/%s is stale", cacheNamespace, cache.Name)
	}

	candidates := catalog.ToCandidates()
	for i := range candidates {
		candidates[i].Spec.ImagePullSecrets = append(
			[]corev1.LocalObjectReference(nil),
			spec.ImagePullSecrets...,
		)
		candidates[i].Spec.ServiceAccountName = spec.ServiceAccountName
	}
	return resolvedProfileSetCandidates(candidates), nil
}

func listClusterProfileCandidatesWithLabels(ctx context.Context, c client.Client, aimID string, provenance labels.Selector) ([]profileSetCandidate, error) {
	var list aimv1alpha2.AIMClusterProfileList
	opts := []client.ListOption{}
	if aimID != "" {
		opts = append(opts, client.MatchingFields{aimv1alpha2.ProfileAimIdIndexKey: aimID})
	}
	if provenance != nil && !provenance.Empty() {
		opts = append(opts, client.MatchingLabelsSelector{Selector: provenance})
	}
	if err := c.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	out := make([]profileSetCandidate, 0, len(list.Items))
	for i := range list.Items {
		if !aimprofile.IsProfileCopyable(list.Items[i].Annotations) {
			continue
		}
		spec := list.Items[i].Spec.AIMProfileSpecCommon
		out = append(out, profileSetCandidate{
			ProfileCopyCandidate: aimprofile.ProfileCopyCandidate{
				Name:      list.Items[i].Name,
				Spec:      spec,
				Status:    list.Items[i].Status,
				BaseImage: aimprofile.BaseImageFromProfile(&list.Items[i], list.Items[i].Status.BaseImage),
			},
			annotations: list.Items[i].Annotations,
			labels:      list.Items[i].Labels,
			sourceKind:  "AIMClusterProfile",
		})
	}
	return out, nil
}

func listNamespaceProfileCandidatesWithLabels(ctx context.Context, c client.Client, namespace, aimID string, provenance labels.Selector) ([]profileSetCandidate, error) {
	var list aimv1alpha2.AIMProfileList
	opts := []client.ListOption{client.InNamespace(namespace)}
	if aimID != "" {
		opts = append(opts, client.MatchingFields{aimv1alpha2.ProfileAimIdIndexKey: aimID})
	}
	if provenance != nil && !provenance.Empty() {
		opts = append(opts, client.MatchingLabelsSelector{Selector: provenance})
	}
	if err := c.List(ctx, &list, opts...); err != nil {
		return nil, err
	}
	out := make([]profileSetCandidate, 0, len(list.Items))
	for i := range list.Items {
		if !aimprofile.IsProfileCopyable(list.Items[i].Annotations) {
			continue
		}
		spec := list.Items[i].Spec.AIMProfileSpecCommon
		out = append(out, profileSetCandidate{
			ProfileCopyCandidate: aimprofile.ProfileCopyCandidate{
				Name:      list.Items[i].Name,
				Spec:      spec,
				Status:    list.Items[i].Status,
				BaseImage: aimprofile.BaseImageFromProfile(&list.Items[i], list.Items[i].Status.BaseImage),
			},
			annotations: list.Items[i].Annotations,
			labels:      list.Items[i].Labels,
			sourceKind:  "AIMProfile",
		})
	}
	return out, nil
}

func resolvedProfileSetCandidates(candidates []aimprofile.ProfileCopyCandidate) []profileSetCandidate {
	resolved := make([]profileSetCandidate, len(candidates))
	for i := range candidates {
		resolved[i] = profileSetCandidate{
			ProfileCopyCandidate: candidates[i],
			labels: map[string]string{
				constants.LabelKeyProfileRole:   aimprofile.ProfileRoleLabelValue(candidates[i].Spec),
				constants.LabelKeyProfileOrigin: string(aimv1alpha1.ProfileOriginDiscovered),
			},
			sourceKind:       "discovery catalog",
			contractResolved: true,
		}
	}
	return resolved
}

// profileSetPruneReady allows stale managed outputs to be removed only after
// every non-empty replacement exists with the desired spec and has reported
// Ready for its current generation. A valid zero-match selection may prune
// immediately because there is no replacement rollout to wait for.
func profileSetPruneReady(desired []desiredProfile, existing []managedProfile) bool {
	desiredNames := desiredProfileNames(desired)
	hasStale := false
	for _, profile := range existing {
		if _, keep := desiredNames[client.ObjectKeyFromObject(profile.Object).String()]; !keep {
			hasStale = true
			break
		}
	}
	if !hasStale || len(desired) == 0 {
		return true
	}

	existingByKey := make(map[string]managedProfile, len(existing))
	for _, profile := range existing {
		existingByKey[client.ObjectKeyFromObject(profile.Object).String()] = profile
	}
	for _, wanted := range desired {
		current, found := existingByKey[client.ObjectKeyFromObject(wanted.Object).String()]
		if !found ||
			current.Object.GetGeneration() <= 0 ||
			current.Status.ObservedGeneration != current.Object.GetGeneration() ||
			current.Status.Status != constants.AIMStatusReady {
			return false
		}
		wantedSpec, wantedOK := profileSpecCommon(wanted.Object)
		currentSpec, currentOK := profileSpecCommon(current.Object)
		if !wantedOK || !currentOK || !aimprofile.ProfileSpecsEquivalent(currentSpec, wantedSpec) {
			return false
		}
	}
	return true
}

func mergeCandidates(clusterCandidates, namespaceCandidates []aimprofile.ProfileCopyCandidate) []aimprofile.ProfileCopyCandidate {
	merged := make(map[string]aimprofile.ProfileCopyCandidate, len(clusterCandidates)+len(namespaceCandidates))
	for _, candidate := range clusterCandidates {
		merged[candidateIdentity(candidate)] = candidate
	}
	for _, candidate := range namespaceCandidates {
		merged[candidateIdentity(candidate)] = candidate
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]aimprofile.ProfileCopyCandidate, 0, len(keys))
	for _, key := range keys {
		out = append(out, merged[key])
	}
	return out
}

func candidateIdentity(candidate aimprofile.ProfileCopyCandidate) string {
	// manualSelectionOnly is intentionally omitted: it is deprecated and no
	// longer affects selection, so two candidates differing only by it are
	// equivalent for dedup purposes.
	identity := fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%s|%t|%s|%s|%s|%d|%s|%s",
		candidate.Name,
		candidate.Spec.AimId,
		candidate.Spec.ModelId,
		candidate.Spec.ProfileId,
		candidate.Spec.Engine,
		candidate.Spec.Metric,
		candidate.Spec.Type,
		candidate.Spec.Primary,
		candidate.Spec.AcceleratorModel,
		candidate.Spec.AcceleratorType,
		candidate.Spec.Precision,
		candidate.Spec.AcceleratorCount,
		candidate.Status.Version,
		engineArgsIdentity(candidate.Spec.EngineArgs),
	)
	// Preserve the exact pre-acceleratorVendor identity for legacy profiles so
	// existing derived profile names remain stable. Explicit vendor contracts
	// extend the identity and therefore cannot collide with each other.
	if candidate.Spec.AcceleratorVendor != "" {
		identity += "|vendor=" + string(candidate.Spec.AcceleratorVendor)
	}
	return identity
}

func engineArgsIdentity(value *apiextensionsv1.JSON) string {
	if value == nil {
		return ""
	}
	return string(value.Raw)
}

func listManagedNamespaceProfiles(ctx context.Context, c client.Client, namespace, profileSetUID string) ([]managedProfile, error) {
	var list aimv1alpha2.AIMProfileList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{ManagedProfileSetUIDIndexKey: profileSetUID},
	); err != nil {
		return nil, err
	}
	out := make([]managedProfile, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, managedProfile{
			Object: list.Items[i].DeepCopy(),
			Status: list.Items[i].Status,
		})
	}
	return out, nil
}

func listManagedClusterProfiles(ctx context.Context, c client.Client, profileSetUID string) ([]managedProfile, error) {
	var list aimv1alpha2.AIMClusterProfileList
	if err := c.List(ctx, &list, client.MatchingFields{ManagedProfileSetUIDIndexKey: profileSetUID}); err != nil {
		return nil, err
	}
	out := make([]managedProfile, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, managedProfile{
			Object: list.Items[i].DeepCopy(),
			Status: list.Items[i].Status,
		})
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func desiredProfileNames(desired []desiredProfile) map[string]struct{} {
	names := make(map[string]struct{}, len(desired))
	for _, profile := range desired {
		names[client.ObjectKeyFromObject(profile.Object).String()] = struct{}{}
	}
	return names
}

func collectErrors(values ...error) []error {
	result := make([]error, 0, len(values))
	for _, err := range values {
		if err != nil {
			result = append(result, err)
		}
	}
	return result
}
