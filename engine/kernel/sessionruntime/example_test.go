package sessionruntime_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	engineid "github.com/stacklok/mecatl/engine/id"
	"github.com/stacklok/mecatl/engine/kernel/groupstorage"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
	groupid "github.com/stacklok/mecatl/engine/kernel/resourcegroup/id"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
	"github.com/stacklok/mecatl/engine/kernel/sessionruntime"
)

// composeTwoResourceKinds is a compile-only host composition sketch, not an
// operational fixture. The constructors currently return ErrNotImplemented;
// nothing below claims that a Group or either resource can actually be opened.
func composeTwoResourceKinds(ctx context.Context, managerConfig resourcegroup.ManagerConfig,
	sessionConfig sessionruntime.FactoryConfig, gid groupid.GroupID, sid sessionid.SessionID,
	environmentPolicy resourcegroup.FactoryPolicy,
) error {
	sessions, err := sessionruntime.NewFactory(sessionConfig)
	if err != nil {
		return err
	}
	manager, err := resourcegroup.NewManager(managerConfig)
	if err != nil {
		return err
	}
	if err := manager.Register(sessions); err != nil {
		return err
	}
	// This existing group is assumed to have no ENV members: otherwise opening
	// before registering its environment factory must fail for an unknown kind.
	group, err := manager.Load(ctx, gid)
	if err != nil {
		return err
	}
	if _, err := sessionruntime.Load(ctx, group, sid); err != nil {
		return err
	}
	if _, _, err := sessionruntime.Create(ctx, group); err != nil {
		return err
	}
	// Additive registration after opening supports a second, independent kind.
	if err := manager.Register(&environmentFactory{policy: environmentPolicy}); err != nil {
		return err
	}
	_, eid, err := createEnvironment(ctx, group, "example/image", map[string]string{"purpose": "build"})
	if err != nil {
		return err
	}
	if _, err := resourcegroup.Load[*environmentController](ctx, group, eid); err != nil {
		return err
	}
	// Permanent membership includes unloaded resources; local residency does not.
	if _, err := group.Members(ctx, "SES", groupstorage.MemberPageRequest{Limit: 10}); err != nil {
		return err
	}
	if _, err := group.ResidentResources(); err != nil {
		return err
	}
	if _, err := manager.ResidentGroups(); err != nil {
		return err
	}
	if err := manager.Drain(); err != nil {
		return err
	}
	select {
	case <-manager.Done():
		_, err := manager.Report()
		return err // Availability is not proof of clean shutdown; inspect the report.
	case <-ctx.Done():
		return ctx.Err() // Only ends waiting, not Manager-owned shutdown.
	}
}

// environmentID belongs to the environment domain, not to resourcegroup.
type environmentID struct{ value engineid.ID }

func (id environmentID) ResourceID() engineid.ID { return id.value }

type environmentController struct{ id environmentID }

func (e *environmentController) ID() environmentID { return e.id }

type environmentFactory struct {
	policy resourcegroup.FactoryPolicy
}

func (*environmentFactory) ResourceType() reflect.Type {
	return reflect.TypeFor[*environmentController]()
}
func (*environmentFactory) Kind() engineid.Kind                   { return "ENV" }
func (f *environmentFactory) Policy() resourcegroup.FactoryPolicy { return f.policy }
func (*environmentFactory) DecodeID(id engineid.ID) (environmentID, error) {
	if id.Kind() != "ENV" {
		return environmentID{}, errors.New("expected environment ID")
	}
	return environmentID{value: id}, nil
}
func (*environmentFactory) Load(*resourcegroup.Scope, environmentID) (resourcegroup.Loaded[*environmentController], error) {
	return resourcegroup.Loaded[*environmentController]{}, resourcegroup.ErrNotImplemented
}

var _ resourcegroup.Loader[*environmentController, environmentID] = (*environmentFactory)(nil)

// createEnvironment has natural domain arguments, unlike Session creation. This
// declaration allocates no ID and neither invokes nor retains dependencies.
func createEnvironment(context.Context, *resourcegroup.Group, string, map[string]string) (*environmentController, environmentID, error) {
	return nil, environmentID{}, resourcegroup.ErrNotImplemented
}

// compileEnvironmentCreateShape illustrates the post-validation allocation and
// initializer seam. Tests exercise it only against the nonoperational group helper:
// it allocates a test identity, but constructs no resource and persists nothing.
// Only T is explicit: Go infers I and the concrete F from the ID and closure.
// The Group resolves the registered factory; the caller cannot substitute one.
func compileEnvironmentCreateShape(ctx context.Context, group *resourcegroup.Group, image string, labels map[string]string) (*environmentController, environmentID, error) {
	labels = maps.Clone(labels)
	id := environmentID{value: engineid.New("ENV")}
	value, err := resourcegroup.Create[*environmentController](ctx, group, id,
		func(factory *environmentFactory, scope *resourcegroup.Scope, id environmentID) (resourcegroup.Loaded[*environmentController], error) {
			return factory.initialize(scope, id, image, labels)
		})
	return value, id, err
}

// initialize stands for domain-specific construction under the supplied Scope.
// It is only a typed seam here: no resource or Completion is created.
func (*environmentFactory) initialize(*resourcegroup.Scope, environmentID, string, map[string]string) (resourcegroup.Loaded[*environmentController], error) {
	return resourcegroup.Loaded[*environmentController]{}, resourcegroup.ErrNotImplemented
}

func TestTwoKindCompositionSkeleton(t *testing.T) {
	// Compilation covers both resource paths; execution stops at NewFactory.
	if err := composeTwoResourceKinds(context.Background(), resourcegroup.ManagerConfig{},
		sessionruntime.FactoryConfig{}, groupid.GroupID{}, sessionid.SessionID{},
		resourcegroup.FactoryPolicy{}); err != resourcegroup.ErrNotImplemented {
		t.Fatalf("composition = %v", err)
	}
	// The typed environment initializer is never invoked by the shared stub.
	value, id, err := compileEnvironmentCreateShape(context.Background(), nil, "example/image", nil)
	if value != nil || id.ResourceID().Kind() != "ENV" || err != resourcegroup.ErrNotImplemented {
		t.Fatalf("environment construction seam = %v, %v, %v", value, id, err)
	}
}
