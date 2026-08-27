package fpoc

import (
	"context"
	"testing"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"

	"gitlab.com/gitlab-org/fleeting/fleeting/provider"

	"github.com/myplixa/openstack-fleeting-plugin/internal/openstackclient"
)

// stubClient implements openstackclient.Client with just enough behavior to
// exercise the Name<->UUID resolution path used by Update/Decrease/ConnectInfo.
type stubClient struct {
	openstackclient.Client

	servers []servers.Server

	deletedIDs       []string
	getServerIDsSeen []string
}

func (s *stubClient) ListServers(ctx context.Context) ([]servers.Server, error) {
	return s.servers, nil
}

func (s *stubClient) GetServer(ctx context.Context, serverId string) (*servers.Server, error) {
	s.getServerIDsSeen = append(s.getServerIDsSeen, serverId)
	for _, srv := range s.servers {
		if srv.ID == serverId {
			cp := srv
			return &cp, nil
		}
	}
	return nil, context.DeadlineExceeded
}

func (s *stubClient) DeleteServer(ctx context.Context, serverId string) error {
	s.deletedIDs = append(s.deletedIDs, serverId)
	return nil
}

// CreateServer mimics real Nova behavior: the create-server response only
// includes a minimal representation (id, adminPass, links) - notably not
// Name, even though the request set one. See createInstance()'s comment.
func (s *stubClient) CreateServer(ctx context.Context, spec servers.CreateOptsBuilder, hintOpts servers.SchedulerHintOptsBuilder) (*servers.Server, error) {
	return &servers.Server{ID: "created-uuid-1234"}, nil
}

func newTestGroup(client openstackclient.Client) *InstanceGroup {
	return &InstanceGroup{
		Name:   "cloud-dc3-ptnad-build-high",
		client: client,
		log:    hclog.NewNullLogger(),
	}
}

func TestUpdateReportsServerNameNotUUID(t *testing.T) {
	stub := &stubClient{
		servers: []servers.Server{
			{
				ID:       "11111111-1111-1111-1111-111111111111",
				Name:     "cloud-dc3-ptnad-build-high-a0b9d91f",
				Status:   "DELETED",
				Metadata: map[string]string{MetadataKey: "cloud-dc3-ptnad-build-high"},
			},
		},
	}
	g := newTestGroup(stub)

	var gotIDs []string
	err := g.Update(context.Background(), func(instance string, state provider.State) {
		gotIDs = append(gotIDs, instance)
	})
	require.NoError(t, err)
	require.Equal(t, []string{"cloud-dc3-ptnad-build-high-a0b9d91f"}, gotIDs)
}

func TestIncrease_UsesRequestNameNotEmptyResponseName(t *testing.T) {
	stub := &stubClient{}
	g := newTestGroup(stub)
	g.settings.UseStaticCredentials = true // skip SSH key injection path

	succeeded, err := g.Increase(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, 1, succeeded)

	var cachedName string
	var cachedID string
	g.nameToID.Range(func(k, v any) bool {
		cachedName = k.(string)
		cachedID = v.(string)
		return false
	})

	require.NotEmpty(t, cachedName, "the generated instance name must be cached, not an empty string from the create response")
	require.Contains(t, cachedName, "cloud-dc3-ptnad-build-high-")
	require.Equal(t, "created-uuid-1234", cachedID)
}

func TestResolveInstanceID_CacheHit(t *testing.T) {
	stub := &stubClient{}
	g := newTestGroup(stub)
	g.nameToID.Store("myname", "myid")

	id, err := g.resolveInstanceID(context.Background(), "myname")
	require.NoError(t, err)
	require.Equal(t, "myid", id)
}

func TestResolveInstanceID_FallbackToListServers(t *testing.T) {
	stub := &stubClient{
		servers: []servers.Server{
			{ID: "uuid-1", Name: "name-1"},
			{ID: "uuid-2", Name: "name-2"},
		},
	}
	g := newTestGroup(stub)

	id, err := g.resolveInstanceID(context.Background(), "name-2")
	require.NoError(t, err)
	require.Equal(t, "uuid-2", id)

	// second call should hit the now-populated cache, not ListServers again
	stub.servers = nil
	id, err = g.resolveInstanceID(context.Background(), "name-2")
	require.NoError(t, err)
	require.Equal(t, "uuid-2", id)
}

func TestResolveInstanceID_NotFound(t *testing.T) {
	stub := &stubClient{}
	g := newTestGroup(stub)

	_, err := g.resolveInstanceID(context.Background(), "does-not-exist")
	require.Error(t, err)
}

func TestDecrease_ResolvesNameToUUIDBeforeDeleting(t *testing.T) {
	stub := &stubClient{}
	g := newTestGroup(stub)
	g.nameToID.Store("cloud-dc3-ptnad-build-high-a0b9d91f", "11111111-1111-1111-1111-111111111111")

	succeeded, err := g.Decrease(context.Background(), []string{"cloud-dc3-ptnad-build-high-a0b9d91f"})
	require.NoError(t, err)
	require.Equal(t, []string{"cloud-dc3-ptnad-build-high-a0b9d91f"}, succeeded)
	require.Equal(t, []string{"11111111-1111-1111-1111-111111111111"}, stub.deletedIDs)

	// cache entry should be cleared after a successful delete
	_, ok := g.nameToID.Load("cloud-dc3-ptnad-build-high-a0b9d91f")
	require.False(t, ok)
}

func TestConnectInfo_ResolvesNameToUUIDAndKeepsNameAsID(t *testing.T) {
	stub := &stubClient{
		servers: []servers.Server{
			{
				ID:         "11111111-1111-1111-1111-111111111111",
				Name:       "cloud-dc3-ptnad-build-high-a0b9d91f",
				Status:     "ACTIVE",
				AccessIPv4: "10.54.69.77",
			},
		},
	}
	g := newTestGroup(stub)
	g.nameToID.Store("cloud-dc3-ptnad-build-high-a0b9d91f", "11111111-1111-1111-1111-111111111111")

	info, err := g.ConnectInfo(context.Background(), "cloud-dc3-ptnad-build-high-a0b9d91f")
	require.NoError(t, err)

	// GitLab Runner keeps tracking this instance by the same string it was
	// given (the Name) - ConnectInfo must echo it back as ID, not the UUID.
	require.Equal(t, "cloud-dc3-ptnad-build-high-a0b9d91f", info.ID)
	require.Equal(t, "10.54.69.77", info.InternalAddr)

	// but the real OpenStack API call underneath must have used the UUID
	require.Equal(t, []string{"11111111-1111-1111-1111-111111111111"}, stub.getServerIDsSeen)
}
