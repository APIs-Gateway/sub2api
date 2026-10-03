//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCurrentImagePermissionBypassesAuthCacheAndRejectsIdentityChanges(t *testing.T) {
	gid := int64(7)
	initial := &APIKey{ID: 3, UserID: 4, Key: "image-key", GroupID: &gid, Status: StatusActive, Group: &Group{ID: gid, Platform: PlatformOpenAI, Hydrated: true, Status: StatusActive, SimpleModeAutoImageEligible: true}}
	for _, tc := range []struct {
		name   string
		change func(*APIKey)
		fail   bool
	}{
		{"current", func(k *APIKey) {}, false},
		{"explicit disable", func(k *APIKey) { k.Group.SimpleModeAutoImageEligible = false }, false},
		{"moved group", func(k *APIKey) { id := int64(9); k.GroupID = &id }, true},
		{"changed platform", func(k *APIKey) { k.Group.Platform = PlatformGrok }, true},
		{"disabled key", func(k *APIKey) { k.Status = StatusDisabled }, true},
		{"disabled group", func(k *APIKey) { k.Group.Status = StatusDisabled }, true},
		{"disabled user", func(k *APIKey) { k.User.Status = StatusDisabled }, true},
		{"replaced key", func(k *APIKey) { k.ID++ }, true},
		{"wrong owner", func(k *APIKey) { k.UserID++ }, true},
		{"missing group", func(k *APIKey) { k.Group = nil }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := *initial
			g := *initial.Group
			current.Group = &g
			current.User = &User{ID: 4, Status: StatusActive}
			tc.change(&current)
			calls := 0
			svc := &APIKeyService{apiKeyRepo: &authRepoStub{getByKeyForAuth: func(ctx context.Context, key string) (*APIKey, error) {
				calls++
				require.Equal(t, initial.Key, key)
				return &current, nil
			}}}
			got, err := svc.GetCurrentImagePermissionGroup(context.Background(), initial)
			if tc.fail {
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, current.Group, got)
			}
			require.Equal(t, 1, calls)
		})
	}
	svc := &APIKeyService{apiKeyRepo: &authRepoStub{getByKeyForAuth: func(context.Context, string) (*APIKey, error) { return nil, errors.New("db failure") }}}
	_, err := svc.GetCurrentImagePermissionGroup(context.Background(), initial)
	require.Error(t, err)
}

func TestSimpleModeImageEligibilityAuthSnapshotRoundtrip(t *testing.T) {
	gid := int64(4)
	svc := &APIKeyService{}
	k := &APIKey{ID: 1, UserID: 2, GroupID: &gid, User: &User{ID: 2, Status: StatusActive}, Group: &Group{ID: gid, Hydrated: true, Status: StatusActive, Platform: PlatformOpenAI, SimpleModeAutoImageEligible: true}}
	snapshot := svc.snapshotFromAPIKey(context.Background(), k)
	require.True(t, snapshot.Group.SimpleModeAutoImageEligible)
	roundtrip := svc.snapshotToAPIKey("key", snapshot)
	require.True(t, roundtrip.Group.SimpleModeAutoImageEligible)
	snapshot.Version = 15
	_, used, err := svc.applyAuthCacheEntry("key", &APIKeyAuthCacheEntry{Snapshot: snapshot})
	require.NoError(t, err)
	require.False(t, used)
}

func TestAdminExplicitImagePermissionClearsEligibilityInBothUpdateEntrypoints(t *testing.T) {
 for _,value:=range []bool{false,true} {
  for _,admin:=range []bool{false,true} {
   group:=&Group{ID:1,Name:"image-default",Platform:PlatformOpenAI,Status:StatusActive,Hydrated:true,SimpleModeAutoImageEligible:true,SubscriptionType:SubscriptionTypeStandard}
   repo:=&groupRepoStubForAdmin{getByID:group}
   if admin {_,err:=(&adminServiceImpl{groupRepo:repo}).UpdateGroup(context.Background(),1,&UpdateGroupInput{AllowImageGeneration:&value});require.NoError(t,err)} else {_,err:=(&GroupService{groupRepo:repo}).Update(context.Background(),1,UpdateGroupRequest{AllowImageGeneration:&value});require.NoError(t,err)}
   require.False(t,group.SimpleModeAutoImageEligible)
   require.Equal(t,value,group.AllowImageGeneration)
  }
 }
 group:=&Group{ID:1,Name:"image-default",Platform:PlatformOpenAI,Status:StatusActive,Hydrated:true,SimpleModeAutoImageEligible:true,SubscriptionType:SubscriptionTypeStandard}
 repo:=&groupRepoStubForAdmin{getByID:group}
 _,err:=(&adminServiceImpl{groupRepo:repo}).UpdateGroup(context.Background(),1,&UpdateGroupInput{Description:func()*string{s:="ordinary edit";return &s}()});require.NoError(t,err);require.True(t,group.SimpleModeAutoImageEligible)
 duplicateRepo:=&duplicateGroupRepoStub{groupRepoStubForAdmin:repo}
 copy,err:=(&adminServiceImpl{groupRepo:duplicateRepo}).DuplicateGroup(context.Background(),1,"admin:1","image-copy");require.NoError(t,err);require.False(t,copy.SimpleModeAutoImageEligible)
}

func TestCurrentImagePermissionPreservesUnassignedKeysAndRejectsNewAssignment(t *testing.T) {
 initial:=&APIKey{ID:1,UserID:2,Key:"unassigned",Status:StatusActive}
 current:=*initial;current.User=&User{ID:2,Status:StatusActive}
 svc:=&APIKeyService{apiKeyRepo:&authRepoStub{getByKeyForAuth:func(context.Context,string)(*APIKey,error){return &current,nil}}}
 group,err:=svc.GetCurrentImagePermissionGroup(context.Background(),initial);require.NoError(t,err);require.Nil(t,group)
 gateway:=&OpenAIGatewayService{}
 hooks:=&OpenAIWSIngressHooks{BeforeImagePermission:func()(*Group,error){return svc.GetCurrentImagePermissionGroup(context.Background(),initial)}}
 require.True(t,gateway.currentWSImagePermission(hooks,initial))
 gid:=int64(3);current.GroupID=&gid;current.Group=&Group{ID:gid,Platform:PlatformOpenAI,Hydrated:true,Status:StatusActive}
 require.False(t,gateway.currentWSImagePermission(hooks,initial))
}
