package loadgen

import (
	"fmt"
	"sync/atomic"

	"github.com/kong/kong-loadgen/src/config"
)

// Attribution buckets that are not generated users. Every request is counted
// in exactly one bucket: a group name, Anonymous, or Invalid-credentials.
const (
	BucketAnonymous = "Anonymous"
	BucketInvalid   = "Invalid-credentials"
)

// Identity is the credential a request carries and the bucket it counts in.
type Identity struct {
	Bucket   string
	Username string
	Key      string
}

func anonymous() Identity {
	return Identity{Bucket: BucketAnonymous}
}

// selector chooses the identity for each request on one endpoint.
type selector interface {
	pick(src *source) Identity
}

type fixedUser struct {
	identity Identity
}

func (f fixedUser) pick(*source) Identity { return f.identity }

// weightedGroups picks a group by weight, then a user in that group at random.
type weightedGroups struct {
	groups []weightedMembers
	total  float64
}

type weightedMembers struct {
	members []Identity
	weight  float64
}

func (w weightedGroups) pick(src *source) Identity {
	target := src.float64() * w.total
	for _, group := range w.groups {
		if target < group.weight {
			return group.members[src.intN(len(group.members))]
		}
		target -= group.weight
	}
	last := w.groups[len(w.groups)-1]
	return last.members[src.intN(len(last.members))]
}

// roundRobin cycles through every listed user in order, for even traffic.
type roundRobin struct {
	identities []Identity
	next       atomic.Uint64
}

func (r *roundRobin) pick(*source) Identity {
	index := (r.next.Add(1) - 1) % uint64(len(r.identities))
	return r.identities[index]
}

// membersOf lists every generated user in a group, in index order.
func membersOf(group config.UserGroup) []Identity {
	members := make([]Identity, 0, group.Count)
	for index := 1; index <= group.Count; index++ {
		members = append(members, Identity{
			Bucket:   group.Name,
			Username: group.Username(index),
			Key:      group.APIKey(index),
		})
	}
	return members
}

// newSelector builds the identity selector for an endpoint, or nil when the
// endpoint has no identity block (auth: none).
func newSelector(identity *config.IdentityConfig, groups []config.UserGroup) (selector, error) {
	if identity == nil {
		return nil, nil
	}
	byName := map[string]config.UserGroup{}
	for _, group := range groups {
		byName[group.Name] = group
	}
	switch identity.Mode {
	case "single-user":
		group, index, found := config.FindUser(groups, identity.User)
		if !found {
			return nil, unknownUserError(identity.User)
		}
		return fixedUser{identity: Identity{Bucket: group.Name, Username: group.Username(index), Key: group.APIKey(index)}}, nil
	case "weighted-groups":
		var selected weightedGroups
		for _, ref := range identity.Groups {
			group, ok := byName[ref.Group]
			if !ok {
				return nil, unknownGroupError(ref.Group)
			}
			selected.groups = append(selected.groups, weightedMembers{members: membersOf(group), weight: float64(ref.Weight)})
			selected.total += float64(ref.Weight)
		}
		if selected.total <= 0 {
			return nil, weightError()
		}
		return selected, nil
	case "round-robin":
		var identities []Identity
		for _, ref := range identity.Groups {
			group, ok := byName[ref.Group]
			if !ok {
				return nil, unknownGroupError(ref.Group)
			}
			identities = append(identities, membersOf(group)...)
		}
		if len(identities) == 0 {
			return nil, weightError()
		}
		return &roundRobin{identities: identities}, nil
	}
	return nil, fmt.Errorf("unknown identity.mode %q", identity.Mode)
}

func unknownUserError(user string) error {
	return fmt.Errorf("identity.user %q does not name a generated user", user)
}

func unknownGroupError(group string) error {
	return fmt.Errorf("identity references unknown user group %q", group)
}

func weightError() error {
	return fmt.Errorf("identity needs at least one group with a positive weight or user")
}
