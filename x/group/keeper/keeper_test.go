package keeper_test

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	tmtime "github.com/tendermint/tendermint/libs/time"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"

	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/testutil/testdata"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/bank/testutil"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/cosmos/cosmos-sdk/x/group/internal/math"
	"github.com/cosmos/cosmos-sdk/x/group/keeper"
	"github.com/cosmos/cosmos-sdk/x/group/module"
)

type TestSuite struct {
	suite.Suite

	app             *simapp.SimApp
	sdkCtx          sdk.Context
	ctx             context.Context
	addrs           []sdk.AccAddress
	groupID         uint64
	groupPolicyAddr sdk.AccAddress
	policy          group.DecisionPolicy
	keeper          keeper.Keeper
	blockTime       time.Time
}

func (s *TestSuite) SetupTest() {
	app := simapp.Setup(s.T(), false)
	ctx := app.BaseApp.NewContext(false, tmproto.Header{})

	s.blockTime = tmtime.Now()
	ctx = ctx.WithBlockHeader(tmproto.Header{Time: s.blockTime})

	s.app = app
	s.sdkCtx = ctx
	s.ctx = sdk.WrapSDKContext(ctx)
	s.keeper = s.app.GroupKeeper
	s.addrs = simapp.AddTestAddrsIncremental(app, ctx, 6, sdk.NewInt(30000000))

	// Initial group, group policy and balance setup
	members := []group.Member{
		{Address: s.addrs[4].String(), Weight: "1"}, {Address: s.addrs[1].String(), Weight: "2"},
	}
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   s.addrs[0].String(),
		Members: members,
	})
	s.Require().NoError(err)
	s.groupID = groupRes.GroupId

	policy := group.NewThresholdDecisionPolicy(
		"2",
		time.Second,
		0,
	)
	policyReq := &group.MsgCreateGroupPolicy{
		Admin:   s.addrs[0].String(),
		GroupId: s.groupID,
	}
	err = policyReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	policyRes, err := s.keeper.CreateGroupPolicy(s.ctx, policyReq)
	s.Require().NoError(err)
	s.policy = policy
	addr, err := sdk.AccAddressFromBech32(policyRes.Address)
	s.Require().NoError(err)
	s.groupPolicyAddr = addr
	s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, s.sdkCtx, s.groupPolicyAddr, sdk.Coins{sdk.NewInt64Coin("test", 10000)}))
}

func TestKeeperTestSuite(t *testing.T) {
	suite.Run(t, new(TestSuite))
}

func (s *TestSuite) TestCreateGroup() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr3 := addrs[2]
	addr5 := addrs[4]
	addr6 := addrs[5]

	members := []group.Member{{
		Address: addr5.String(),
		Weight:  "1",
		AddedAt: s.blockTime,
	}, {
		Address: addr6.String(),
		Weight:  "2",
		AddedAt: s.blockTime,
	}}

	expGroups := []*group.GroupInfo{
		{
			Id:          s.groupID,
			Version:     1,
			Admin:       addr1.String(),
			TotalWeight: "3",
			CreatedAt:   s.blockTime,
		},
		{
			Id:          2,
			Version:     1,
			Admin:       addr1.String(),
			TotalWeight: "3",
			CreatedAt:   s.blockTime,
		},
	}

	specs := map[string]struct {
		req       *group.MsgCreateGroup
		expErr    bool
		expGroups []*group.GroupInfo
	}{
		"all good": {
			req: &group.MsgCreateGroup{
				Admin:   addr1.String(),
				Members: members,
			},
			expGroups: expGroups,
		},
		"group metadata too long": {
			req: &group.MsgCreateGroup{
				Admin:    addr1.String(),
				Members:  members,
				Metadata: strings.Repeat("a", 256),
			},
			expErr: true,
		},
		"member metadata too long": {
			req: &group.MsgCreateGroup{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address:  addr3.String(),
					Weight:   "1",
					Metadata: strings.Repeat("a", 256),
				}},
			},
			expErr: true,
		},
		"zero member weight": {
			req: &group.MsgCreateGroup{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address: addr3.String(),
					Weight:  "0",
				}},
			},
			expErr: true,
		},
	}

	var seq uint32 = 1
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			res, err := s.keeper.CreateGroup(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				_, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: uint64(seq + 1)})
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			id := res.GroupId

			seq++
			s.Assert().Equal(uint64(seq), id)

			// then all data persisted
			loadedGroupRes, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: id})
			s.Require().NoError(err)
			s.Assert().Equal(spec.req.Admin, loadedGroupRes.Info.Admin)
			s.Assert().Equal(spec.req.Metadata, loadedGroupRes.Info.Metadata)
			s.Assert().Equal(id, loadedGroupRes.Info.Id)
			s.Assert().Equal(uint64(1), loadedGroupRes.Info.Version)

			// and members are stored as well
			membersRes, err := s.keeper.GroupMembers(s.ctx, &group.QueryGroupMembersRequest{GroupId: id})
			s.Require().NoError(err)
			loadedMembers := membersRes.Members
			s.Require().Equal(len(members), len(loadedMembers))
			// we reorder members by address to be able to compare them
			sort.Slice(members, func(i, j int) bool {
				addri, err := sdk.AccAddressFromBech32(members[i].Address)
				s.Require().NoError(err)
				addrj, err := sdk.AccAddressFromBech32(members[j].Address)
				s.Require().NoError(err)
				return bytes.Compare(addri, addrj) < 0
			})
			for i := range loadedMembers {
				s.Assert().Equal(members[i].Metadata, loadedMembers[i].Member.Metadata)
				s.Assert().Equal(members[i].Address, loadedMembers[i].Member.Address)
				s.Assert().Equal(members[i].Weight, loadedMembers[i].Member.Weight)
				s.Assert().Equal(members[i].AddedAt, loadedMembers[i].Member.AddedAt)
				s.Assert().Equal(id, loadedMembers[i].GroupId)
			}

			// query groups by admin
			groupsRes, err := s.keeper.GroupsByAdmin(s.ctx, &group.QueryGroupsByAdminRequest{Admin: addr1.String()})
			s.Require().NoError(err)
			loadedGroups := groupsRes.Groups
			s.Require().Equal(len(spec.expGroups), len(loadedGroups))
			for i := range loadedGroups {
				s.Assert().Equal(spec.expGroups[i].Metadata, loadedGroups[i].Metadata)
				s.Assert().Equal(spec.expGroups[i].Admin, loadedGroups[i].Admin)
				s.Assert().Equal(spec.expGroups[i].TotalWeight, loadedGroups[i].TotalWeight)
				s.Assert().Equal(spec.expGroups[i].Id, loadedGroups[i].Id)
				s.Assert().Equal(spec.expGroups[i].Version, loadedGroups[i].Version)
				s.Assert().Equal(spec.expGroups[i].CreatedAt, loadedGroups[i].CreatedAt)
			}
		})
	}

}

func (s *TestSuite) TestUpdateGroupAdmin() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr3 := addrs[2]
	addr4 := addrs[3]

	members := []group.Member{{
		Address: addr1.String(),
		Weight:  "1",
		AddedAt: s.blockTime,
	}}
	oldAdmin := addr2.String()
	newAdmin := addr3.String()
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   oldAdmin,
		Members: members,
	})
	s.Require().NoError(err)
	groupID := groupRes.GroupId
	specs := map[string]struct {
		req       *group.MsgUpdateGroupAdmin
		expStored *group.GroupInfo
		expErr    bool
	}{
		"with correct admin": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  groupID,
				Admin:    oldAdmin,
				NewAdmin: newAdmin,
			},
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       newAdmin,
				TotalWeight: "1",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  groupID,
				Admin:    addr4.String(),
				NewAdmin: newAdmin,
			},
			expErr: true,
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       oldAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
		},
		"with unknown groupID": {
			req: &group.MsgUpdateGroupAdmin{
				GroupId:  999,
				Admin:    oldAdmin,
				NewAdmin: newAdmin,
			},
			expErr: true,
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       oldAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			_, err := s.keeper.UpdateGroupAdmin(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expStored, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupMetadata() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr3 := addrs[2]

	oldAdmin := addr1.String()
	groupID := s.groupID

	specs := map[string]struct {
		req       *group.MsgUpdateGroupMetadata
		expErr    bool
		expStored *group.GroupInfo
	}{
		"with correct admin": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId: groupID,
				Admin:   oldAdmin,
			},
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       oldAdmin,
				TotalWeight: "3",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId: groupID,
				Admin:   addr3.String(),
			},
			expErr: true,
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       oldAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
		},
		"with unknown groupid": {
			req: &group.MsgUpdateGroupMetadata{
				GroupId: 999,
				Admin:   oldAdmin,
			},
			expErr: true,
			expStored: &group.GroupInfo{
				Id:          groupID,
				Admin:       oldAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx, _ := s.sdkCtx.CacheContext()
			ctx := sdk.WrapSDKContext(sdkCtx)
			_, err := s.keeper.UpdateGroupMetadata(ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.keeper.GroupInfo(ctx, &group.QueryGroupInfoRequest{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expStored, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupMembers() {
	addrs := s.addrs
	addr3 := addrs[2]
	addr4 := addrs[3]
	addr5 := addrs[4]
	addr6 := addrs[5]

	member1 := addr5.String()
	member2 := addr6.String()
	members := []group.Member{{
		Address: member1,
		Weight:  "1",
	}}

	myAdmin := addr4.String()
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   myAdmin,
		Members: members,
	})
	s.Require().NoError(err)
	groupID := groupRes.GroupId

	specs := map[string]struct {
		req        *group.MsgUpdateGroupMembers
		expErr     bool
		expGroup   *group.GroupInfo
		expMembers []*group.GroupMember
	}{
		"add new member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member2,
					Weight:  "2",
				}},
			},
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "3",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{
				{
					Member: &group.Member{
						Address: member2,
						Weight:  "2",
					},
					GroupId: groupID,
				},
				{
					Member: &group.Member{
						Address: member1,
						Weight:  "1",
					},
					GroupId: groupID,
				},
			},
		},
		"update member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "2",
				}},
			},
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "2",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{
				{
					GroupId: groupID,
					Member: &group.Member{
						Address: member1,
						Weight:  "2",
					},
				},
			},
		},
		"update member with same data": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "1",
				}},
			},
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "1",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{
				{
					GroupId: groupID,
					Member: &group.Member{
						Address: member1,
						Weight:  "1",
					},
				},
			},
		},
		"replace member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{
					{
						Address: member1,
						Weight:  "0",
					},
					{
						Address: member2,
						Weight:  "1",
					},
				},
			},
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "1",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member2,
					Weight:  "1",
				},
			}},
		},
		"remove existing member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "0",
				}},
			},
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "0",
				Version:     2,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{},
		},
		"remove unknown member": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: addr4.String(),
					Weight:  "0",
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member1,
					Weight:  "1",
				},
			}},
		},
		"with wrong admin": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: groupID,
				Admin:   addr3.String(),
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "2",
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member1,
					Weight:  "1",
				},
			}},
		},
		"with unknown groupID": {
			req: &group.MsgUpdateGroupMembers{
				GroupId: 999,
				Admin:   myAdmin,
				MemberUpdates: []group.Member{{
					Address: member1,
					Weight:  "2",
				}},
			},
			expErr: true,
			expGroup: &group.GroupInfo{
				Id:          groupID,
				Admin:       myAdmin,
				TotalWeight: "1",
				Version:     1,
				CreatedAt:   s.blockTime,
			},
			expMembers: []*group.GroupMember{{
				GroupId: groupID,
				Member: &group.Member{
					Address: member1,
					Weight:  "1",
				},
			}},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx, _ := s.sdkCtx.CacheContext()
			ctx := sdk.WrapSDKContext(sdkCtx)
			_, err := s.keeper.UpdateGroupMembers(ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			// then
			res, err := s.keeper.GroupInfo(ctx, &group.QueryGroupInfoRequest{GroupId: groupID})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroup, res.Info)

			// and members persisted
			membersRes, err := s.keeper.GroupMembers(ctx, &group.QueryGroupMembersRequest{GroupId: groupID})
			s.Require().NoError(err)
			loadedMembers := membersRes.Members
			s.Require().Equal(len(spec.expMembers), len(loadedMembers))
			// we reorder group members by address to be able to compare them
			sort.Slice(spec.expMembers, func(i, j int) bool {
				addri, err := sdk.AccAddressFromBech32(spec.expMembers[i].Member.Address)
				s.Require().NoError(err)
				addrj, err := sdk.AccAddressFromBech32(spec.expMembers[j].Member.Address)
				s.Require().NoError(err)
				return bytes.Compare(addri, addrj) < 0
			})
			for i := range loadedMembers {
				s.Assert().Equal(spec.expMembers[i].Member.Metadata, loadedMembers[i].Member.Metadata)
				s.Assert().Equal(spec.expMembers[i].Member.Address, loadedMembers[i].Member.Address)
				s.Assert().Equal(spec.expMembers[i].Member.Weight, loadedMembers[i].Member.Weight)
				s.Assert().Equal(spec.expMembers[i].GroupId, loadedMembers[i].GroupId)
			}
		})
	}
}

func (s *TestSuite) TestCreateGroupWithPolicy() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr3 := addrs[2]
	addr5 := addrs[4]
	addr6 := addrs[5]

	members := []group.Member{{
		Address: addr5.String(),
		Weight:  "1",
		AddedAt: s.blockTime,
	}, {
		Address: addr6.String(),
		Weight:  "2",
		AddedAt: s.blockTime,
	}}

	specs := map[string]struct {
		req       *group.MsgCreateGroupWithPolicy
		policy    group.DecisionPolicy
		expErr    bool
		expErrMsg string
	}{
		"all good": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin:              addr1.String(),
				Members:            members,
				GroupPolicyAsAdmin: false,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
		},
		"group policy as admin is true": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin:              addr1.String(),
				Members:            members,
				GroupPolicyAsAdmin: true,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
		},
		"group metadata too long": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin:              addr1.String(),
				Members:            members,
				GroupPolicyAsAdmin: false,
				GroupMetadata:      strings.Repeat("a", 256),
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "limit exceeded",
		},
		"group policy metadata too long": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin:               addr1.String(),
				Members:             members,
				GroupPolicyAsAdmin:  false,
				GroupPolicyMetadata: strings.Repeat("a", 256),
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "limit exceeded",
		},
		"member metadata too long": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address:  addr3.String(),
					Weight:   "1",
					Metadata: strings.Repeat("a", 256),
				}},
				GroupPolicyAsAdmin: false,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "limit exceeded",
		},
		"zero member weight": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin: addr1.String(),
				Members: []group.Member{{
					Address: addr3.String(),
					Weight:  "0",
				}},
				GroupPolicyAsAdmin: false,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "expected a positive decimal",
		},
		"decision policy threshold > total group weight": {
			req: &group.MsgCreateGroupWithPolicy{
				Admin:              addr1.String(),
				Members:            members,
				GroupPolicyAsAdmin: false,
			},
			policy: group.NewThresholdDecisionPolicy(
				"10",
				time.Second,
				0,
			),
			expErr: false,
		},
	}

	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			err := spec.req.SetDecisionPolicy(spec.policy)
			s.Require().NoError(err)

			res, err := s.keeper.CreateGroupWithPolicy(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				s.Require().Contains(err.Error(), spec.expErrMsg)
				return
			}
			s.Require().NoError(err)
			id := res.GroupId
			groupPolicyAddr := res.GroupPolicyAddress

			// then all data persisted in group
			loadedGroupRes, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: id})
			s.Require().NoError(err)
			s.Assert().Equal(spec.req.GroupMetadata, loadedGroupRes.Info.Metadata)
			s.Assert().Equal(id, loadedGroupRes.Info.Id)
			if spec.req.GroupPolicyAsAdmin {
				s.Assert().NotEqual(spec.req.Admin, loadedGroupRes.Info.Admin)
				s.Assert().Equal(groupPolicyAddr, loadedGroupRes.Info.Admin)
			} else {
				s.Assert().Equal(spec.req.Admin, loadedGroupRes.Info.Admin)
			}

			// and members are stored as well
			membersRes, err := s.keeper.GroupMembers(s.ctx, &group.QueryGroupMembersRequest{GroupId: id})
			s.Require().NoError(err)
			loadedMembers := membersRes.Members
			s.Require().Equal(len(members), len(loadedMembers))
			// we reorder members by address to be able to compare them
			sort.Slice(members, func(i, j int) bool {
				addri, err := sdk.AccAddressFromBech32(members[i].Address)
				s.Require().NoError(err)
				addrj, err := sdk.AccAddressFromBech32(members[j].Address)
				s.Require().NoError(err)
				return bytes.Compare(addri, addrj) < 0
			})
			for i := range loadedMembers {
				s.Assert().Equal(members[i].Metadata, loadedMembers[i].Member.Metadata)
				s.Assert().Equal(members[i].Address, loadedMembers[i].Member.Address)
				s.Assert().Equal(members[i].Weight, loadedMembers[i].Member.Weight)
				s.Assert().Equal(members[i].AddedAt, loadedMembers[i].Member.AddedAt)
				s.Assert().Equal(id, loadedMembers[i].GroupId)
			}

			// then all data persisted in group policy
			groupPolicyRes, err := s.keeper.GroupPolicyInfo(s.ctx, &group.QueryGroupPolicyInfoRequest{Address: groupPolicyAddr})
			s.Require().NoError(err)

			groupPolicy := groupPolicyRes.Info
			s.Assert().Equal(groupPolicyAddr, groupPolicy.Address)
			s.Assert().Equal(id, groupPolicy.GroupId)
			s.Assert().Equal(spec.req.GroupPolicyMetadata, groupPolicy.Metadata)
			s.Assert().Equal(spec.policy.(*group.ThresholdDecisionPolicy), groupPolicy.GetDecisionPolicy())
			if spec.req.GroupPolicyAsAdmin {
				s.Assert().NotEqual(spec.req.Admin, groupPolicy.Admin)
				s.Assert().Equal(groupPolicyAddr, groupPolicy.Admin)
			} else {
				s.Assert().Equal(spec.req.Admin, groupPolicy.Admin)
			}
		})
	}

}

func (s *TestSuite) TestCreateGroupPolicy() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr4 := addrs[3]

	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   addr1.String(),
		Members: nil,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	specs := map[string]struct {
		req       *group.MsgCreateGroupPolicy
		policy    group.DecisionPolicy
		expErr    bool
		expErrMsg string
	}{
		"all good": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
		},
		"all good with percentage decision policy": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: myGroupID,
			},
			policy: group.NewPercentageDecisionPolicy(
				"0.5",
				time.Second,
				0,
			),
		},
		"decision policy threshold > total group weight": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"10",
				time.Second,
				0,
			),
		},
		"group id does not exists": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: 9999,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "not found",
		},
		"admin not group admin": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr4.String(),
				GroupId: myGroupID,
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "not group admin",
		},
		"metadata too long": {
			req: &group.MsgCreateGroupPolicy{
				Admin:    addr1.String(),
				GroupId:  myGroupID,
				Metadata: strings.Repeat("a", 256),
			},
			policy: group.NewThresholdDecisionPolicy(
				"1",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "limit exceeded",
		},
		"percentage decision policy with negative value": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: myGroupID,
			},
			policy: group.NewPercentageDecisionPolicy(
				"-0.5",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "expected a positive decimal",
		},
		"percentage decision policy with value greater than 1": {
			req: &group.MsgCreateGroupPolicy{
				Admin:   addr1.String(),
				GroupId: myGroupID,
			},
			policy: group.NewPercentageDecisionPolicy(
				"2",
				time.Second,
				0,
			),
			expErr:    true,
			expErrMsg: "percentage must be > 0 and <= 1",
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			err := spec.req.SetDecisionPolicy(spec.policy)
			s.Require().NoError(err)

			res, err := s.keeper.CreateGroupPolicy(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				s.Require().Contains(err.Error(), spec.expErrMsg)
				return
			}
			s.Require().NoError(err)
			addr := res.Address

			// then all data persisted
			groupPolicyRes, err := s.keeper.GroupPolicyInfo(s.ctx, &group.QueryGroupPolicyInfoRequest{Address: addr})
			s.Require().NoError(err)

			groupPolicy := groupPolicyRes.Info
			s.Assert().Equal(addr, groupPolicy.Address)
			s.Assert().Equal(myGroupID, groupPolicy.GroupId)
			s.Assert().Equal(spec.req.Admin, groupPolicy.Admin)
			s.Assert().Equal(spec.req.Metadata, groupPolicy.Metadata)
			s.Assert().Equal(uint64(1), groupPolicy.Version)
			percentageDecisionPolicy, ok := spec.policy.(*group.PercentageDecisionPolicy)
			if ok {
				s.Assert().Equal(percentageDecisionPolicy, groupPolicy.GetDecisionPolicy())
			} else {
				s.Assert().Equal(spec.policy.(*group.ThresholdDecisionPolicy), groupPolicy.GetDecisionPolicy())
			}
		})
	}
}

func (s *TestSuite) TestUpdateGroupPolicyAdmin() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr5 := addrs[4]

	admin, newAdmin := addr1, addr2
	policy := group.NewThresholdDecisionPolicy(
		"1",
		time.Second,
		0,
	)
	groupPolicyAddr, myGroupID := s.createGroupAndGroupPolicy(admin, nil, policy)

	specs := map[string]struct {
		req            *group.MsgUpdateGroupPolicyAdmin
		expGroupPolicy *group.GroupPolicyInfo
		expErr         bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupPolicyAdmin{
				Admin:              addr5.String(),
				GroupPolicyAddress: groupPolicyAddr,
				NewAdmin:           newAdmin.String(),
			},
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          admin.String(),
				Address:        groupPolicyAddr,
				GroupId:        myGroupID,
				Version:        2,
				DecisionPolicy: nil,
				CreatedAt:      s.blockTime,
			},
			expErr: true,
		},
		"with wrong group policy": {
			req: &group.MsgUpdateGroupPolicyAdmin{
				Admin:              admin.String(),
				GroupPolicyAddress: addr5.String(),
				NewAdmin:           newAdmin.String(),
			},
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          admin.String(),
				Address:        groupPolicyAddr,
				GroupId:        myGroupID,
				Version:        2,
				DecisionPolicy: nil,
				CreatedAt:      s.blockTime,
			},
			expErr: true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupPolicyAdmin{
				Admin:              admin.String(),
				GroupPolicyAddress: groupPolicyAddr,
				NewAdmin:           newAdmin.String(),
			},
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          newAdmin.String(),
				Address:        groupPolicyAddr,
				GroupId:        myGroupID,
				Version:        2,
				DecisionPolicy: nil,
				CreatedAt:      s.blockTime,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		err := spec.expGroupPolicy.SetDecisionPolicy(policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.keeper.UpdateGroupPolicyAdmin(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.keeper.GroupPolicyInfo(s.ctx, &group.QueryGroupPolicyInfoRequest{
				Address: groupPolicyAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupPolicy, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupPolicyMetadata() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr5 := addrs[4]

	admin := addr1
	policy := group.NewThresholdDecisionPolicy(
		"1",
		time.Second,
		0,
	)
	groupPolicyAddr, myGroupID := s.createGroupAndGroupPolicy(admin, nil, policy)

	specs := map[string]struct {
		req            *group.MsgUpdateGroupPolicyMetadata
		expGroupPolicy *group.GroupPolicyInfo
		expErr         bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupPolicyMetadata{
				Admin:              addr5.String(),
				GroupPolicyAddress: groupPolicyAddr,
			},
			expGroupPolicy: &group.GroupPolicyInfo{},
			expErr:         true,
		},
		"with wrong group policy": {
			req: &group.MsgUpdateGroupPolicyMetadata{
				Admin:              admin.String(),
				GroupPolicyAddress: addr5.String(),
			},
			expGroupPolicy: &group.GroupPolicyInfo{},
			expErr:         true,
		},
		"with comment too long": {
			req: &group.MsgUpdateGroupPolicyMetadata{
				Admin:              admin.String(),
				GroupPolicyAddress: addr5.String(),
			},
			expGroupPolicy: &group.GroupPolicyInfo{},
			expErr:         true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupPolicyMetadata{
				Admin:              admin.String(),
				GroupPolicyAddress: groupPolicyAddr,
			},
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          admin.String(),
				Address:        groupPolicyAddr,
				GroupId:        myGroupID,
				Version:        2,
				DecisionPolicy: nil,
				CreatedAt:      s.blockTime,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		err := spec.expGroupPolicy.SetDecisionPolicy(policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.keeper.UpdateGroupPolicyMetadata(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.keeper.GroupPolicyInfo(s.ctx, &group.QueryGroupPolicyInfoRequest{
				Address: groupPolicyAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupPolicy, res.Info)
		})
	}
}

func (s *TestSuite) TestUpdateGroupPolicyDecisionPolicy() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr5 := addrs[4]

	admin := addr1
	policy := group.NewThresholdDecisionPolicy(
		"1",
		time.Second,
		0,
	)
	groupPolicyAddr, myGroupID := s.createGroupAndGroupPolicy(admin, nil, policy)

	specs := map[string]struct {
		preRun         func(admin sdk.AccAddress) (policyAddr string, groupId uint64)
		req            *group.MsgUpdateGroupPolicyDecisionPolicy
		policy         group.DecisionPolicy
		expGroupPolicy *group.GroupPolicyInfo
		expErr         bool
	}{
		"with wrong admin": {
			req: &group.MsgUpdateGroupPolicyDecisionPolicy{
				Admin:              addr5.String(),
				GroupPolicyAddress: groupPolicyAddr,
			},
			policy:         policy,
			expGroupPolicy: &group.GroupPolicyInfo{},
			expErr:         true,
		},
		"with wrong group policy": {
			req: &group.MsgUpdateGroupPolicyDecisionPolicy{
				Admin:              admin.String(),
				GroupPolicyAddress: addr5.String(),
			},
			policy:         policy,
			expGroupPolicy: &group.GroupPolicyInfo{},
			expErr:         true,
		},
		"correct data": {
			req: &group.MsgUpdateGroupPolicyDecisionPolicy{
				Admin:              admin.String(),
				GroupPolicyAddress: groupPolicyAddr,
			},
			policy: group.NewThresholdDecisionPolicy(
				"2",
				time.Duration(2)*time.Second,
				0,
			),
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          admin.String(),
				Address:        groupPolicyAddr,
				GroupId:        myGroupID,
				Version:        2,
				DecisionPolicy: nil,
				CreatedAt:      s.blockTime,
			},
			expErr: false,
		},
		"correct data with percentage decision policy": {
			preRun: func(admin sdk.AccAddress) (string, uint64) {
				return s.createGroupAndGroupPolicy(admin, nil, policy)
			},
			req: &group.MsgUpdateGroupPolicyDecisionPolicy{
				Admin:              admin.String(),
				GroupPolicyAddress: groupPolicyAddr,
			},
			policy: group.NewPercentageDecisionPolicy(
				"0.5",
				time.Duration(2)*time.Second,
				0,
			),
			expGroupPolicy: &group.GroupPolicyInfo{
				Admin:          admin.String(),
				DecisionPolicy: nil,
				Version:        2,
				CreatedAt:      s.blockTime,
			},
			expErr: false,
		},
	}
	for msg, spec := range specs {
		spec := spec
		policyAddr := groupPolicyAddr
		err := spec.expGroupPolicy.SetDecisionPolicy(spec.policy)
		s.Require().NoError(err)
		if spec.preRun != nil {
			policyAddr1, groupId := spec.preRun(admin)
			policyAddr = policyAddr1

			// update the expected info with new group policy details
			spec.expGroupPolicy.Address = policyAddr1
			spec.expGroupPolicy.GroupId = groupId

			// update req with new group policy addr
			spec.req.GroupPolicyAddress = policyAddr1
		}

		err = spec.req.SetDecisionPolicy(spec.policy)
		s.Require().NoError(err)

		s.Run(msg, func() {
			_, err := s.keeper.UpdateGroupPolicyDecisionPolicy(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			res, err := s.keeper.GroupPolicyInfo(s.ctx, &group.QueryGroupPolicyInfoRequest{
				Address: policyAddr,
			})
			s.Require().NoError(err)
			s.Assert().Equal(spec.expGroupPolicy, res.Info)
		})
	}
}

func (s *TestSuite) TestGroupPoliciesByAdminOrGroup() {
	addrs := s.addrs
	addr2 := addrs[1]

	admin := addr2
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   admin.String(),
		Members: nil,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	policies := []group.DecisionPolicy{
		group.NewThresholdDecisionPolicy(
			"1",
			time.Second,
			0,
		),
		group.NewThresholdDecisionPolicy(
			"10",
			time.Second,
			0,
		),
		group.NewPercentageDecisionPolicy(
			"0.5",
			time.Second,
			0,
		),
	}

	count := 3
	expectAccs := make([]*group.GroupPolicyInfo, count)
	for i := range expectAccs {
		req := &group.MsgCreateGroupPolicy{
			Admin:   admin.String(),
			GroupId: myGroupID,
		}
		err := req.SetDecisionPolicy(policies[i])
		s.Require().NoError(err)
		res, err := s.keeper.CreateGroupPolicy(s.ctx, req)
		s.Require().NoError(err)

		expectAcc := &group.GroupPolicyInfo{
			Address:   res.Address,
			Admin:     admin.String(),
			GroupId:   myGroupID,
			Version:   uint64(1),
			CreatedAt: s.blockTime,
		}
		err = expectAcc.SetDecisionPolicy(policies[i])
		s.Require().NoError(err)
		expectAccs[i] = expectAcc
	}
	sort.Slice(expectAccs, func(i, j int) bool { return expectAccs[i].Address < expectAccs[j].Address })

	// query group policy by group
	policiesByGroupRes, err := s.keeper.GroupPoliciesByGroup(s.ctx, &group.QueryGroupPoliciesByGroupRequest{
		GroupId: myGroupID,
	})
	s.Require().NoError(err)
	policyAccs := policiesByGroupRes.GroupPolicies
	s.Require().Equal(len(policyAccs), count)
	// we reorder policyAccs by address to be able to compare them
	sort.Slice(policyAccs, func(i, j int) bool { return policyAccs[i].Address < policyAccs[j].Address })
	for i := range policyAccs {
		s.Assert().Equal(policyAccs[i].Address, expectAccs[i].Address)
		s.Assert().Equal(policyAccs[i].GroupId, expectAccs[i].GroupId)
		s.Assert().Equal(policyAccs[i].Admin, expectAccs[i].Admin)
		s.Assert().Equal(policyAccs[i].Metadata, expectAccs[i].Metadata)
		s.Assert().Equal(policyAccs[i].Version, expectAccs[i].Version)
		s.Assert().Equal(policyAccs[i].CreatedAt, expectAccs[i].CreatedAt)
		s.Assert().Equal(policyAccs[i].GetDecisionPolicy(), expectAccs[i].GetDecisionPolicy())
	}

	// query group policy by admin
	policiesByAdminRes, err := s.keeper.GroupPoliciesByAdmin(s.ctx, &group.QueryGroupPoliciesByAdminRequest{
		Admin: admin.String(),
	})
	s.Require().NoError(err)
	policyAccs = policiesByAdminRes.GroupPolicies
	s.Require().Equal(len(policyAccs), count)
	// we reorder policyAccs by address to be able to compare them
	sort.Slice(policyAccs, func(i, j int) bool { return policyAccs[i].Address < policyAccs[j].Address })
	for i := range policyAccs {
		s.Assert().Equal(policyAccs[i].Address, expectAccs[i].Address)
		s.Assert().Equal(policyAccs[i].GroupId, expectAccs[i].GroupId)
		s.Assert().Equal(policyAccs[i].Admin, expectAccs[i].Admin)
		s.Assert().Equal(policyAccs[i].Metadata, expectAccs[i].Metadata)
		s.Assert().Equal(policyAccs[i].Version, expectAccs[i].Version)
		s.Assert().Equal(policyAccs[i].CreatedAt, expectAccs[i].CreatedAt)
		s.Assert().Equal(policyAccs[i].GetDecisionPolicy(), expectAccs[i].GetDecisionPolicy())
	}
}

func (s *TestSuite) TestSubmitProposal() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr4 := addrs[3]
	addr5 := addrs[4]

	myGroupID := s.groupID
	accountAddr := s.groupPolicyAddr

	msgSend := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}

	policyReq := &group.MsgCreateGroupPolicy{
		Admin:   addr1.String(),
		GroupId: myGroupID,
	}
	policy := group.NewThresholdDecisionPolicy(
		"100",
		time.Second,
		0,
	)
	err := policyReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	bigThresholdRes, err := s.keeper.CreateGroupPolicy(s.ctx, policyReq)
	s.Require().NoError(err)
	bigThresholdAddr := bigThresholdRes.Address

	defaultProposal := group.Proposal{
		GroupPolicyAddress: accountAddr.String(),
		Status:             group.PROPOSAL_STATUS_SUBMITTED,
		FinalTallyResult: group.TallyResult{
			YesCount:        "0",
			NoCount:         "0",
			AbstainCount:    "0",
			NoWithVetoCount: "0",
		},
		ExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
	}
	specs := map[string]struct {
		req         *group.MsgSubmitProposal
		msgs        []sdk.Msg
		expProposal group.Proposal
		expErr      bool
		postRun     func(sdkCtx sdk.Context)
	}{
		"all good with minimal fields set": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String()},
			},
			expProposal: defaultProposal,
			postRun:     func(sdkCtx sdk.Context) {},
		},
		"all good with good msg payload": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String()},
			},
			msgs: []sdk.Msg{&banktypes.MsgSend{
				FromAddress: accountAddr.String(),
				ToAddress:   addr2.String(),
				Amount:      sdk.Coins{sdk.NewInt64Coin("token", 100)},
			}},
			expProposal: defaultProposal,
			postRun:     func(sdkCtx sdk.Context) {},
		},
		"metadata too long": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String()},
				Metadata:           strings.Repeat("a", 256),
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"group policy required": {
			req: &group.MsgSubmitProposal{
				Proposers: []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"existing group policy required": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: addr1.String(),
				Proposers:          []string{addr2.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"decision policy threshold > total group weight": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: bigThresholdAddr,
				Proposers:          []string{addr2.String()},
			},
			expErr: false,
			expProposal: group.Proposal{
				GroupPolicyAddress: bigThresholdAddr,
				Status:             group.PROPOSAL_STATUS_SUBMITTED,
				FinalTallyResult:   group.DefaultTallyResult(),
				ExecutorResult:     group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			},
			postRun: func(sdkCtx sdk.Context) {},
		},
		"only group members can create a proposal": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr4.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"all proposers must be in group": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String(), addr4.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"admin that is not a group member can not create proposal": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr1.String()},
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"reject msgs that are not authz by group policy": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String()},
			},
			msgs:    []sdk.Msg{&testdata.TestMsg{Signers: []string{addr1.String()}}},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"with try exec": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr2.String()},
				Exec:               group.Exec_EXEC_TRY,
			},
			msgs: []sdk.Msg{msgSend},
			expProposal: group.Proposal{
				GroupPolicyAddress: accountAddr.String(),
				Status:             group.PROPOSAL_STATUS_ACCEPTED,
				FinalTallyResult: group.TallyResult{
					YesCount:        "2",
					NoCount:         "0",
					AbstainCount:    "0",
					NoWithVetoCount: "0",
				},
				ExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
			},
			postRun: func(sdkCtx sdk.Context) {
				fromBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, accountAddr)
				s.Require().Contains(fromBalances, sdk.NewInt64Coin("test", 9900))
				toBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, addr2)
				s.Require().Contains(toBalances, sdk.NewInt64Coin("test", 100))
			},
		},
		"with try exec, not enough yes votes for proposal to pass": {
			req: &group.MsgSubmitProposal{
				GroupPolicyAddress: accountAddr.String(),
				Proposers:          []string{addr5.String()},
				Exec:               group.Exec_EXEC_TRY,
			},
			msgs: []sdk.Msg{msgSend},
			expProposal: group.Proposal{
				GroupPolicyAddress: accountAddr.String(),
				Status:             group.PROPOSAL_STATUS_SUBMITTED,
				FinalTallyResult: group.TallyResult{
					YesCount:        "0", // Since tally doesn't pass Allow(), we consider the proposal not final
					NoCount:         "0",
					AbstainCount:    "0",
					NoWithVetoCount: "0",
				},
				ExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			},
			postRun: func(sdkCtx sdk.Context) {},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			err := spec.req.SetMsgs(spec.msgs)
			s.Require().NoError(err)

			res, err := s.keeper.SubmitProposal(s.ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)
			id := res.ProposalId

			if !(spec.expProposal.ExecutorResult == group.PROPOSAL_EXECUTOR_RESULT_SUCCESS) {
				// then all data persisted
				proposalRes, err := s.keeper.Proposal(s.ctx, &group.QueryProposalRequest{ProposalId: id})
				s.Require().NoError(err)
				proposal := proposalRes.Proposal

				s.Assert().Equal(spec.expProposal.GroupPolicyAddress, proposal.GroupPolicyAddress)
				s.Assert().Equal(spec.req.Metadata, proposal.Metadata)
				s.Assert().Equal(spec.req.Proposers, proposal.Proposers)
				s.Assert().Equal(s.blockTime, proposal.SubmitTime)
				s.Assert().Equal(uint64(1), proposal.GroupVersion)
				s.Assert().Equal(uint64(1), proposal.GroupPolicyVersion)
				s.Assert().Equal(spec.expProposal.Status, proposal.Status)
				s.Assert().Equal(spec.expProposal.FinalTallyResult, proposal.FinalTallyResult)
				s.Assert().Equal(spec.expProposal.ExecutorResult, proposal.ExecutorResult)
				s.Assert().Equal(s.blockTime.Add(time.Second), proposal.VotingPeriodEnd)

				if spec.msgs == nil { // then empty list is ok
					s.Assert().Len(proposal.GetMsgs(), 0)
				} else {
					s.Assert().Equal(spec.msgs, proposal.GetMsgs())
				}
			}

			spec.postRun(s.sdkCtx)
		})
	}
}

func (s *TestSuite) TestWithdrawProposal() {
	addrs := s.addrs
	addr2 := addrs[1]
	addr5 := addrs[4]
	groupPolicy := s.groupPolicyAddr

	msgSend := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}

	proposers := []string{addr2.String()}
	proposalID := submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)

	specs := map[string]struct {
		preRun     func(sdkCtx sdk.Context) uint64
		proposalId uint64
		admin      string
		expErrMsg  string
	}{
		"wrong admin": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
			},
			admin:     addr5.String(),
			expErrMsg: "unauthorized",
		},
		"wrong proposalId": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return 1111
			},
			admin:     proposers[0],
			expErrMsg: "not found",
		},
		"happy case with proposer": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
			},
			proposalId: proposalID,
			admin:      proposers[0],
		},
		"already closed proposal": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				pId := submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
				_, err := s.keeper.WithdrawProposal(s.ctx, &group.MsgWithdrawProposal{
					ProposalId: pId,
					Address:    proposers[0],
				})
				s.Require().NoError(err)
				return pId
			},
			proposalId: proposalID,
			admin:      proposers[0],
			expErrMsg:  "cannot withdraw a proposal with the status of PROPOSAL_STATUS_WITHDRAWN",
		},
		"happy case with group admin address": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
			},
			proposalId: proposalID,
			admin:      groupPolicy.String(),
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			pId := spec.preRun(s.sdkCtx)

			_, err := s.keeper.WithdrawProposal(s.ctx, &group.MsgWithdrawProposal{
				ProposalId: pId,
				Address:    spec.admin,
			})

			if spec.expErrMsg != "" {
				s.Require().Error(err)
				s.Require().Contains(err.Error(), spec.expErrMsg)
				return
			}

			s.Require().NoError(err)
			resp, err := s.keeper.Proposal(s.ctx, &group.QueryProposalRequest{ProposalId: pId})
			s.Require().NoError(err)
			s.Require().Equal(resp.GetProposal().Status, group.PROPOSAL_STATUS_WITHDRAWN)
		})
	}
}

func (s *TestSuite) TestVote() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]
	addr3 := addrs[2]
	addr4 := addrs[3]
	addr5 := addrs[4]
	members := []group.Member{
		{Address: addr4.String(), Weight: "1", AddedAt: s.blockTime},
		{Address: addr3.String(), Weight: "2", AddedAt: s.blockTime},
	}
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   addr1.String(),
		Members: members,
	})
	s.Require().NoError(err)
	myGroupID := groupRes.GroupId

	policy := group.NewThresholdDecisionPolicy(
		"2",
		time.Duration(2),
		0,
	)
	policyReq := &group.MsgCreateGroupPolicy{
		Admin:   addr1.String(),
		GroupId: myGroupID,
	}
	err = policyReq.SetDecisionPolicy(policy)
	s.Require().NoError(err)
	policyRes, err := s.keeper.CreateGroupPolicy(s.ctx, policyReq)
	s.Require().NoError(err)
	accountAddr := policyRes.Address
	groupPolicy, err := sdk.AccAddressFromBech32(accountAddr)
	s.Require().NoError(err)
	s.Require().NotNil(groupPolicy)

	s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, s.sdkCtx, groupPolicy, sdk.Coins{sdk.NewInt64Coin("test", 10000)}))

	req := &group.MsgSubmitProposal{
		GroupPolicyAddress: accountAddr,
		Proposers:          []string{addr4.String()},
		Messages:           nil,
	}
	err = req.SetMsgs([]sdk.Msg{&banktypes.MsgSend{
		FromAddress: accountAddr,
		ToAddress:   addr5.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}})
	s.Require().NoError(err)

	proposalRes, err := s.keeper.SubmitProposal(s.ctx, req)
	s.Require().NoError(err)
	myProposalID := proposalRes.ProposalId

	// proposals by group policy
	proposalsRes, err := s.keeper.ProposalsByGroupPolicy(s.ctx, &group.QueryProposalsByGroupPolicyRequest{
		Address: accountAddr,
	})
	s.Require().NoError(err)
	proposals := proposalsRes.Proposals
	s.Require().Equal(len(proposals), 1)
	s.Assert().Equal(req.GroupPolicyAddress, proposals[0].GroupPolicyAddress)
	s.Assert().Equal(req.Metadata, proposals[0].Metadata)
	s.Assert().Equal(req.Proposers, proposals[0].Proposers)
	s.Assert().Equal(s.blockTime, proposals[0].SubmitTime)
	s.Assert().Equal(uint64(1), proposals[0].GroupVersion)
	s.Assert().Equal(uint64(1), proposals[0].GroupPolicyVersion)
	s.Assert().Equal(group.PROPOSAL_STATUS_SUBMITTED, proposals[0].Status)
	s.Assert().Equal(group.DefaultTallyResult(), proposals[0].FinalTallyResult)

	specs := map[string]struct {
		srcCtx            sdk.Context
		expTallyResult    group.TallyResult // expected after tallying
		isFinal           bool              // is the tally result final?
		req               *group.MsgVote
		doBefore          func(ctx context.Context)
		postRun           func(sdkCtx sdk.Context)
		expProposalStatus group.ProposalStatus         // expected after tallying
		expExecutorResult group.ProposalExecutorResult // expected after tallying
		expErr            bool
	}{
		"vote yes": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_YES,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "1",
				NoCount:         "0",
				AbstainCount:    "0",
				NoWithVetoCount: "0",
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"with try exec": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr3.String(),
				Option:     group.VOTE_OPTION_YES,
				Exec:       group.Exec_EXEC_TRY,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "2",
				NoCount:         "0",
				AbstainCount:    "0",
				NoWithVetoCount: "0",
			},
			isFinal:           true,
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
			postRun: func(sdkCtx sdk.Context) {
				fromBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, groupPolicy)
				s.Require().Contains(fromBalances, sdk.NewInt64Coin("test", 9900))
				toBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, addr5)
				s.Require().Contains(toBalances, sdk.NewInt64Coin("test", 100))
			},
		},
		"with try exec, not enough yes votes for proposal to pass": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_YES,
				Exec:       group.Exec_EXEC_TRY,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "1",
				NoCount:         "0",
				AbstainCount:    "0",
				NoWithVetoCount: "0",
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote no": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "0",
				NoCount:         "1",
				AbstainCount:    "0",
				NoWithVetoCount: "0",
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote abstain": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_ABSTAIN,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "0",
				NoCount:         "0",
				AbstainCount:    "1",
				NoWithVetoCount: "0",
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"vote veto": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO_WITH_VETO,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "0",
				NoCount:         "0",
				AbstainCount:    "0",
				NoWithVetoCount: "1",
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"apply decision policy early": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr3.String(),
				Option:     group.VOTE_OPTION_YES,
			},
			expTallyResult: group.TallyResult{
				YesCount:        "2",
				NoCount:         "0",
				AbstainCount:    "0",
				NoWithVetoCount: "0",
			},
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
			postRun:           func(sdkCtx sdk.Context) {},
		},
		"reject new votes when final decision is made already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_YES,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.keeper.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr3.String(),
					Option:     group.VOTE_OPTION_NO_WITH_VETO,
					Exec:       1, // Execute the proposal so that its status is final
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"metadata too long": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
				Metadata:   strings.Repeat("a", 256),
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"existing proposal required": {
			req: &group.MsgVote{
				ProposalId: 999,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"empty vote option": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"invalid vote option": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     5,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voter must be in group": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr2.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"admin that is not a group member can not vote": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr1.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"on voting period end": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			srcCtx:  s.sdkCtx.WithBlockTime(s.blockTime.Add(time.Second)),
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"closed already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.keeper.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr3.String(),
					Option:     group.VOTE_OPTION_YES,
					Exec:       1, // Execute to close the proposal.
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
		"voted already": {
			req: &group.MsgVote{
				ProposalId: myProposalID,
				Voter:      addr4.String(),
				Option:     group.VOTE_OPTION_NO,
			},
			doBefore: func(ctx context.Context) {
				_, err := s.keeper.Vote(ctx, &group.MsgVote{
					ProposalId: myProposalID,
					Voter:      addr4.String(),
					Option:     group.VOTE_OPTION_YES,
				})
				s.Require().NoError(err)
			},
			expErr:  true,
			postRun: func(sdkCtx sdk.Context) {},
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx := s.sdkCtx
			if !spec.srcCtx.IsZero() {
				sdkCtx = spec.srcCtx
			}
			sdkCtx, _ = sdkCtx.CacheContext()
			ctx := sdk.WrapSDKContext(sdkCtx)

			if spec.doBefore != nil {
				spec.doBefore(ctx)
			}
			_, err := s.keeper.Vote(ctx, spec.req)
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			s.Require().NoError(err)

			if !(spec.expExecutorResult == group.PROPOSAL_EXECUTOR_RESULT_SUCCESS) {
				// vote is stored and all data persisted
				res, err := s.keeper.VoteByProposalVoter(ctx, &group.QueryVoteByProposalVoterRequest{
					ProposalId: spec.req.ProposalId,
					Voter:      spec.req.Voter,
				})
				s.Require().NoError(err)
				loaded := res.Vote
				s.Assert().Equal(spec.req.ProposalId, loaded.ProposalId)
				s.Assert().Equal(spec.req.Voter, loaded.Voter)
				s.Assert().Equal(spec.req.Option, loaded.Option)
				s.Assert().Equal(spec.req.Metadata, loaded.Metadata)
				s.Assert().Equal(s.blockTime, loaded.SubmitTime)

				// query votes by proposal
				votesByProposalRes, err := s.keeper.VotesByProposal(ctx, &group.QueryVotesByProposalRequest{
					ProposalId: spec.req.ProposalId,
				})
				s.Require().NoError(err)
				votesByProposal := votesByProposalRes.Votes
				s.Require().Equal(1, len(votesByProposal))
				vote := votesByProposal[0]
				s.Assert().Equal(spec.req.ProposalId, vote.ProposalId)
				s.Assert().Equal(spec.req.Voter, vote.Voter)
				s.Assert().Equal(spec.req.Option, vote.Option)
				s.Assert().Equal(spec.req.Metadata, vote.Metadata)
				s.Assert().Equal(s.blockTime, vote.SubmitTime)

				// query votes by voter
				voter := spec.req.Voter
				votesByVoterRes, err := s.keeper.VotesByVoter(ctx, &group.QueryVotesByVoterRequest{
					Voter: voter,
				})
				s.Require().NoError(err)
				votesByVoter := votesByVoterRes.Votes
				s.Require().Equal(1, len(votesByVoter))
				s.Assert().Equal(spec.req.ProposalId, votesByVoter[0].ProposalId)
				s.Assert().Equal(voter, votesByVoter[0].Voter)
				s.Assert().Equal(spec.req.Option, votesByVoter[0].Option)
				s.Assert().Equal(spec.req.Metadata, votesByVoter[0].Metadata)
				s.Assert().Equal(s.blockTime, votesByVoter[0].SubmitTime)

				proposalRes, err := s.keeper.Proposal(ctx, &group.QueryProposalRequest{
					ProposalId: spec.req.ProposalId,
				})
				s.Require().NoError(err)

				proposal := proposalRes.Proposal
				if spec.isFinal {
					s.Assert().Equal(spec.expTallyResult, proposal.FinalTallyResult)
					s.Assert().Equal(spec.expProposalStatus, proposal.Status)
					s.Assert().Equal(spec.expExecutorResult, proposal.ExecutorResult)
				} else {
					s.Assert().Equal(group.DefaultTallyResult(), proposal.FinalTallyResult) // Make sure proposal isn't mutated.

					// do a round of tallying
					tallyResult, err := s.keeper.Tally(sdkCtx, *proposal, myGroupID)
					s.Require().NoError(err)

					s.Assert().Equal(spec.expTallyResult, tallyResult)
				}
			}

			spec.postRun(sdkCtx)
		})
	}

	s.T().Log("test tally result should not take into account the member who left the group")
	require := s.Require()
	members = []group.Member{
		{Address: addr2.String(), Weight: "3", AddedAt: s.blockTime},
		{Address: addr3.String(), Weight: "2", AddedAt: s.blockTime},
		{Address: addr4.String(), Weight: "1", AddedAt: s.blockTime},
	}
	reqCreate := &group.MsgCreateGroupWithPolicy{
		Admin:         addr1.String(),
		Members:       members,
		GroupMetadata: "metadata",
	}

	policy = group.NewThresholdDecisionPolicy(
		"4",
		time.Duration(10),
		0,
	)
	require.NoError(reqCreate.SetDecisionPolicy(policy))
	result, err := s.keeper.CreateGroupWithPolicy(s.ctx, reqCreate)
	require.NoError(err)
	require.NotNil(result)

	policyAddr := result.GroupPolicyAddress
	groupID := result.GroupId
	reqProposal := &group.MsgSubmitProposal{
		GroupPolicyAddress: policyAddr,
		Proposers:          []string{addr4.String()},
	}
	require.NoError(reqProposal.SetMsgs([]sdk.Msg{&banktypes.MsgSend{
		FromAddress: policyAddr,
		ToAddress:   addr5.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}}))

	resSubmitProposal, err := s.keeper.SubmitProposal(s.ctx, reqProposal)
	require.NoError(err)
	require.NotNil(resSubmitProposal)
	proposalID := resSubmitProposal.ProposalId

	for _, voter := range []string{addr4.String(), addr3.String(), addr2.String()} {
		_, err := s.keeper.Vote(s.ctx,
			&group.MsgVote{ProposalId: proposalID, Voter: voter, Option: group.VOTE_OPTION_YES},
		)
		require.NoError(err)
	}

	qProposals, err := s.keeper.Proposal(s.ctx, &group.QueryProposalRequest{
		ProposalId: proposalID,
	})
	require.NoError(err)

	tallyResult, err := s.keeper.Tally(s.sdkCtx, *qProposals.Proposal, groupID)
	require.NoError(err)

	_, err = s.keeper.LeaveGroup(s.ctx, &group.MsgLeaveGroup{Address: addr4.String(), GroupId: groupID})
	require.NoError(err)

	tallyResult1, err := s.keeper.Tally(s.sdkCtx, *qProposals.Proposal, groupID)
	require.NoError(err)
	require.NotEqual(tallyResult.String(), tallyResult1.String())
}

func (s *TestSuite) TestExecProposal() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]

	msgSend1 := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}
	msgSend2 := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 10001)},
	}
	proposers := []string{addr2.String()}

	specs := map[string]struct {
		srcBlockTime      time.Time
		setupProposal     func(ctx context.Context) uint64
		expErr            bool
		expProposalStatus group.ProposalStatus
		expExecutorResult group.ProposalExecutorResult
		expBalance        bool
		expFromBalances   sdk.Coin
		expToBalances     sdk.Coin
	}{
		"proposal executed when accepted": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
			expBalance:        true,
			expFromBalances:   sdk.NewInt64Coin("test", 9900),
			expToBalances:     sdk.NewInt64Coin("test", 100),
		},
		"proposal with multiple messages executed when accepted": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
			expBalance:        true,
			expFromBalances:   sdk.NewInt64Coin("test", 9800),
			expToBalances:     sdk.NewInt64Coin("test", 200),
		},
		"proposal not executed when rejected": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_NO)
			},
			expProposalStatus: group.PROPOSAL_STATUS_REJECTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"open proposal must not fail": {
			setupProposal: func(ctx context.Context) uint64 {
				return submitProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)
			},
			expProposalStatus: group.PROPOSAL_STATUS_SUBMITTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"existing proposal required": {
			setupProposal: func(ctx context.Context) uint64 {
				return 9999
			},
			expErr: true,
		},
		"Decision policy also applied on timeout": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_NO)
			},
			srcBlockTime:      s.blockTime.Add(time.Second),
			expProposalStatus: group.PROPOSAL_STATUS_REJECTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"Decision policy also applied after timeout": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_NO)
			},
			srcBlockTime:      s.blockTime.Add(time.Second).Add(time.Millisecond),
			expProposalStatus: group.PROPOSAL_STATUS_REJECTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"prevent double execution when successful": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := submitProposalAndVote(ctx, s, []sdk.Msg{msgSend1}, proposers, group.VOTE_OPTION_YES)

				_, err := s.keeper.Exec(ctx, &group.MsgExec{Executor: addr1.String(), ProposalId: myProposalID})
				s.Require().NoError(err)
				return myProposalID
			},
			expErr:            true, // since proposal is pruned after a successful MsgExec
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
			expBalance:        true,
			expFromBalances:   sdk.NewInt64Coin("test", 9900),
			expToBalances:     sdk.NewInt64Coin("test", 100),
		},
		"rollback all msg updates on failure": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend2}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_FAILURE,
		},
		"executable when failed before": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend2}
				myProposalID := submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)

				_, err := s.keeper.Exec(ctx, &group.MsgExec{Executor: addr1.String(), ProposalId: myProposalID})
				s.Require().NoError(err)
				sdkCtx := sdk.UnwrapSDKContext(ctx)
				s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, sdkCtx, s.groupPolicyAddr, sdk.Coins{sdk.NewInt64Coin("test", 10002)}))

				return myProposalID
			},
			expProposalStatus: group.PROPOSAL_STATUS_ACCEPTED,
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx, _ := s.sdkCtx.CacheContext()
			ctx := sdk.WrapSDKContext(sdkCtx)
			proposalID := spec.setupProposal(ctx)

			if !spec.srcBlockTime.IsZero() {
				sdkCtx = sdkCtx.WithBlockTime(spec.srcBlockTime)
			}

			ctx = sdk.WrapSDKContext(sdkCtx)
			_, err := s.keeper.Exec(ctx, &group.MsgExec{Executor: addr1.String(), ProposalId: proposalID})
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			if !(spec.expExecutorResult == group.PROPOSAL_EXECUTOR_RESULT_SUCCESS) {

				// and proposal is updated
				res, err := s.keeper.Proposal(ctx, &group.QueryProposalRequest{ProposalId: proposalID})
				s.Require().NoError(err)
				proposal := res.Proposal

				exp := group.ProposalStatus_name[int32(spec.expProposalStatus)]
				got := group.ProposalStatus_name[int32(proposal.Status)]
				s.Assert().Equal(exp, got)

				exp = group.ProposalExecutorResult_name[int32(spec.expExecutorResult)]
				got = group.ProposalExecutorResult_name[int32(proposal.ExecutorResult)]
				s.Assert().Equal(exp, got)
			}

			if spec.expBalance {
				fromBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, s.groupPolicyAddr)
				s.Require().Contains(fromBalances, spec.expFromBalances)
				toBalances := s.app.BankKeeper.GetAllBalances(sdkCtx, addr2)
				s.Require().Contains(toBalances, spec.expToBalances)
			}
		})
	}
}

func (s *TestSuite) TestExecPrunedProposalsAndVotes() {
	addrs := s.addrs
	addr1 := addrs[0]
	addr2 := addrs[1]

	msgSend1 := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}
	msgSend2 := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 10001)},
	}
	proposers := []string{addr2.String()}
	specs := map[string]struct {
		srcBlockTime      time.Time
		setupProposal     func(ctx context.Context) uint64
		expErr            bool
		expErrMsg         string
		expExecutorResult group.ProposalExecutorResult
	}{
		"proposal pruned after executor result success": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expErrMsg:         "load proposal: not found",
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
		},
		"proposal with multiple messages pruned when executed with result success": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expErrMsg:         "load proposal: not found",
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
		},
		"proposal not pruned when not executed and rejected": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_NO)
			},
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"open proposal is not pruned which must not fail ": {
			setupProposal: func(ctx context.Context) uint64 {
				return submitProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)
			},
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"proposal not pruned with group modified before tally": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := submitProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)

				// then modify group
				_, err := s.keeper.UpdateGroupMetadata(ctx, &group.MsgUpdateGroupMetadata{
					Admin:   addr1.String(),
					GroupId: s.groupID,
				})
				s.Require().NoError(err)
				return myProposalID
			},
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"proposal not pruned with group policy modified before tally": {
			setupProposal: func(ctx context.Context) uint64 {
				myProposalID := submitProposal(ctx, s, []sdk.Msg{msgSend1}, proposers)
				_, err := s.keeper.UpdateGroupPolicyMetadata(ctx, &group.MsgUpdateGroupPolicyMetadata{
					Admin:              addr1.String(),
					GroupPolicyAddress: s.groupPolicyAddr.String(),
				})
				s.Require().NoError(err)
				return myProposalID
			},
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_NOT_RUN,
		},
		"proposal exists when rollback all msg updates on failure": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend1, msgSend2}
				return submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)
			},
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_FAILURE,
		},
		"pruned when proposal is executable when failed before": {
			setupProposal: func(ctx context.Context) uint64 {
				msgs := []sdk.Msg{msgSend2}
				myProposalID := submitProposalAndVote(ctx, s, msgs, proposers, group.VOTE_OPTION_YES)

				_, err := s.keeper.Exec(ctx, &group.MsgExec{Executor: addr1.String(), ProposalId: myProposalID})
				s.Require().NoError(err)
				sdkCtx := sdk.UnwrapSDKContext(ctx)
				s.Require().NoError(testutil.FundAccount(s.app.BankKeeper, sdkCtx, s.groupPolicyAddr, sdk.Coins{sdk.NewInt64Coin("test", 10002)}))

				return myProposalID
			},
			expErrMsg:         "load proposal: not found",
			expExecutorResult: group.PROPOSAL_EXECUTOR_RESULT_SUCCESS,
		},
	}
	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			sdkCtx, _ := s.sdkCtx.CacheContext()
			ctx := sdk.WrapSDKContext(sdkCtx)
			proposalID := spec.setupProposal(ctx)

			if !spec.srcBlockTime.IsZero() {
				sdkCtx = sdkCtx.WithBlockTime(spec.srcBlockTime)
			}

			ctx = sdk.WrapSDKContext(sdkCtx)
			_, err := s.keeper.Exec(ctx, &group.MsgExec{Executor: addr1.String(), ProposalId: proposalID})
			if spec.expErr {
				s.Require().Error(err)
				return
			}
			s.Require().NoError(err)

			if spec.expExecutorResult == group.PROPOSAL_EXECUTOR_RESULT_SUCCESS {
				// Make sure proposal is deleted from state
				_, err := s.keeper.Proposal(ctx, &group.QueryProposalRequest{ProposalId: proposalID})
				s.Require().Contains(err.Error(), spec.expErrMsg)
				res, err := s.keeper.VotesByProposal(ctx, &group.QueryVotesByProposalRequest{ProposalId: proposalID})
				s.Require().NoError(err)
				s.Require().Empty(res.GetVotes())

			} else {
				// Check that proposal and votes exists
				res, err := s.keeper.Proposal(ctx, &group.QueryProposalRequest{ProposalId: proposalID})
				s.Require().NoError(err)
				_, err = s.keeper.VotesByProposal(ctx, &group.QueryVotesByProposalRequest{ProposalId: res.Proposal.Id})
				s.Require().NoError(err)
				s.Require().Equal("", spec.expErrMsg)

				exp := group.ProposalExecutorResult_name[int32(spec.expExecutorResult)]
				got := group.ProposalExecutorResult_name[int32(res.Proposal.ExecutorResult)]
				s.Assert().Equal(exp, got)
			}
		})
	}
}

func (s *TestSuite) TestProposalsByVPEnd() {
	addrs := s.addrs
	addr2 := addrs[1]
	groupPolicy := s.groupPolicyAddr

	votingPeriod := s.policy.GetVotingPeriod()
	ctx := s.sdkCtx
	now := time.Now()

	msgSend := &banktypes.MsgSend{
		FromAddress: s.groupPolicyAddr.String(),
		ToAddress:   addr2.String(),
		Amount:      sdk.Coins{sdk.NewInt64Coin("test", 100)},
	}

	proposers := []string{addr2.String()}

	specs := map[string]struct {
		preRun     func(sdkCtx sdk.Context) uint64
		proposalId uint64
		admin      string
		expErrMsg  string
		newCtx     sdk.Context
		tallyRes   group.TallyResult
		expStatus  group.ProposalStatus
	}{
		"tally updated after voting power end": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposal(sdkCtx, s, []sdk.Msg{msgSend}, proposers)
			},
			admin:     proposers[0],
			newCtx:    ctx.WithBlockTime(now.Add(votingPeriod).Add(time.Hour)),
			tallyRes:  group.DefaultTallyResult(),
			expStatus: group.PROPOSAL_STATUS_SUBMITTED,
		},
		"tally within voting period": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
			},
			admin:     proposers[0],
			newCtx:    ctx,
			tallyRes:  group.DefaultTallyResult(),
			expStatus: group.PROPOSAL_STATUS_SUBMITTED,
		},
		"tally within voting period(with votes)": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposalAndVote(s.ctx, s, []sdk.Msg{msgSend}, proposers, group.VOTE_OPTION_YES)
			},
			admin:     proposers[0],
			newCtx:    ctx,
			tallyRes:  group.DefaultTallyResult(),
			expStatus: group.PROPOSAL_STATUS_SUBMITTED,
		},
		"tally after voting period(with votes)": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				return submitProposalAndVote(s.ctx, s, []sdk.Msg{msgSend}, proposers, group.VOTE_OPTION_YES)
			},
			admin:  proposers[0],
			newCtx: ctx.WithBlockTime(now.Add(votingPeriod).Add(time.Hour)),
			tallyRes: group.TallyResult{
				YesCount:        "2",
				NoCount:         "0",
				NoWithVetoCount: "0",
				AbstainCount:    "0",
			},
			expStatus: group.PROPOSAL_STATUS_ACCEPTED,
		},
		"tally of closed proposal": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				pId := submitProposal(s.ctx, s, []sdk.Msg{msgSend}, proposers)
				_, err := s.keeper.WithdrawProposal(s.ctx, &group.MsgWithdrawProposal{
					ProposalId: pId,
					Address:    groupPolicy.String(),
				})

				s.Require().NoError(err)
				return pId
			},
			admin:     proposers[0],
			newCtx:    ctx,
			tallyRes:  group.DefaultTallyResult(),
			expStatus: group.PROPOSAL_STATUS_WITHDRAWN,
		},
		"tally of closed proposal (with votes)": {
			preRun: func(sdkCtx sdk.Context) uint64 {
				pId := submitProposalAndVote(s.ctx, s, []sdk.Msg{msgSend}, proposers, group.VOTE_OPTION_YES)
				_, err := s.keeper.WithdrawProposal(s.ctx, &group.MsgWithdrawProposal{
					ProposalId: pId,
					Address:    groupPolicy.String(),
				})

				s.Require().NoError(err)
				return pId
			},
			admin:     proposers[0],
			newCtx:    ctx,
			tallyRes:  group.DefaultTallyResult(),
			expStatus: group.PROPOSAL_STATUS_WITHDRAWN,
		},
	}

	for msg, spec := range specs {
		spec := spec
		s.Run(msg, func() {
			pId := spec.preRun(s.sdkCtx)

			module.EndBlocker(spec.newCtx, s.keeper)
			resp, err := s.keeper.Proposal(spec.newCtx, &group.QueryProposalRequest{
				ProposalId: pId,
			})

			if spec.expErrMsg != "" {
				s.Require().Error(err)
				s.Require().Contains(err.Error(), spec.expErrMsg)
				return
			}

			s.Require().NoError(err)
			s.Require().Equal(resp.GetProposal().FinalTallyResult, spec.tallyRes)
			s.Require().Equal(resp.GetProposal().Status, spec.expStatus)
		})
	}
}

func (s *TestSuite) TestLeaveGroup() {
	addrs := simapp.AddTestAddrsIncremental(s.app, s.sdkCtx, 7, sdk.NewInt(3000000))
	admin1 := addrs[0]
	member1 := addrs[1]
	member2 := addrs[2]
	member3 := addrs[3]
	member4 := addrs[4]
	admin2 := addrs[5]
	admin3 := addrs[6]
	require := s.Require()

	members := []group.Member{
		{
			Address:  member1.String(),
			Weight:   "1",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
		{
			Address:  member2.String(),
			Weight:   "2",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
		{
			Address:  member3.String(),
			Weight:   "3",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
	}
	policy := group.NewThresholdDecisionPolicy(
		"3",
		time.Hour,
		time.Hour,
	)
	_, groupID1 := s.createGroupAndGroupPolicy(admin1, members, policy)

	members = []group.Member{
		{
			Address:  member1.String(),
			Weight:   "1",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
	}
	_, groupID2 := s.createGroupAndGroupPolicy(admin2, members, nil)

	members = []group.Member{
		{
			Address:  member1.String(),
			Weight:   "1",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
		{
			Address:  member2.String(),
			Weight:   "2",
			Metadata: "metadata",
			AddedAt:  s.sdkCtx.BlockTime(),
		},
	}
	policy = &group.PercentageDecisionPolicy{
		Percentage: "0.5",
		Windows:    &group.DecisionPolicyWindows{VotingPeriod: time.Hour},
	}

	_, groupID3 := s.createGroupAndGroupPolicy(admin3, members, policy)
	testCases := []struct {
		name           string
		req            *group.MsgLeaveGroup
		expErr         bool
		errMsg         string
		expMembersSize int
		memberWeight   math.Dec
	}{
		{
			"expect error: group not found",
			&group.MsgLeaveGroup{
				GroupId: 100000,
				Address: member1.String(),
			},
			true,
			"group: not found",
			0,
			math.NewDecFromInt64(0),
		},
		{
			"expect error: member not part of group",
			&group.MsgLeaveGroup{
				GroupId: groupID1,
				Address: member4.String(),
			},
			true,
			"not part of group",
			0,
			math.NewDecFromInt64(0),
		},
		{
			"valid testcase: decision policy is not present",
			&group.MsgLeaveGroup{
				GroupId: groupID2,
				Address: member1.String(),
			},
			false,
			"",
			0,
			math.NewDecFromInt64(1),
		},
		{
			"valid testcase: threshold decision policy",
			&group.MsgLeaveGroup{
				GroupId: groupID1,
				Address: member3.String(),
			},
			false,
			"",
			2,
			math.NewDecFromInt64(3),
		},
		{
			"valid request: can leave group policy threshold more than group weight",
			&group.MsgLeaveGroup{
				GroupId: groupID1,
				Address: member2.String(),
			},
			false,
			"",
			1,
			math.NewDecFromInt64(2),
		},
		{
			"valid request: can leave group (percentage decision policy)",
			&group.MsgLeaveGroup{
				GroupId: groupID3,
				Address: member2.String(),
			},
			false,
			"",
			1,
			math.NewDecFromInt64(2),
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			var groupWeight1 math.Dec
			if !tc.expErr {
				groupRes, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: tc.req.GroupId})
				require.NoError(err)
				groupWeight1, err = math.NewNonNegativeDecFromString(groupRes.Info.TotalWeight)
				require.NoError(err)
			}

			res, err := s.keeper.LeaveGroup(s.ctx, tc.req)
			if tc.expErr {
				require.Error(err)
				require.Contains(err.Error(), tc.errMsg)
			} else {
				require.NoError(err)
				require.NotNil(res)
				res, err := s.keeper.GroupMembers(s.ctx, &group.QueryGroupMembersRequest{
					GroupId: tc.req.GroupId,
				})
				require.NoError(err)
				require.Len(res.Members, tc.expMembersSize)

				groupRes, err := s.keeper.GroupInfo(s.ctx, &group.QueryGroupInfoRequest{GroupId: tc.req.GroupId})
				require.NoError(err)
				groupWeight2, err := math.NewNonNegativeDecFromString(groupRes.Info.TotalWeight)
				require.NoError(err)

				rWeight, err := groupWeight1.Sub(tc.memberWeight)
				require.NoError(err)
				require.Equal(rWeight.Cmp(groupWeight2), 0)
			}
		})
	}
}

func submitProposal(
	ctx context.Context, s *TestSuite, msgs []sdk.Msg,
	proposers []string) uint64 {
	proposalReq := &group.MsgSubmitProposal{
		GroupPolicyAddress: s.groupPolicyAddr.String(),
		Proposers:          proposers,
	}
	err := proposalReq.SetMsgs(msgs)
	s.Require().NoError(err)

	proposalRes, err := s.keeper.SubmitProposal(ctx, proposalReq)
	s.Require().NoError(err)
	return proposalRes.ProposalId
}

func submitProposalAndVote(
	ctx context.Context, s *TestSuite, msgs []sdk.Msg,
	proposers []string, voteOption group.VoteOption) uint64 {
	s.Require().Greater(len(proposers), 0)
	myProposalID := submitProposal(ctx, s, msgs, proposers)

	_, err := s.keeper.Vote(ctx, &group.MsgVote{
		ProposalId: myProposalID,
		Voter:      proposers[0],
		Option:     voteOption,
	})
	s.Require().NoError(err)
	return myProposalID
}

func (s *TestSuite) createGroupAndGroupPolicy(
	admin sdk.AccAddress,
	members []group.Member,
	policy group.DecisionPolicy,
) (policyAddr string, groupID uint64) {
	groupRes, err := s.keeper.CreateGroup(s.ctx, &group.MsgCreateGroup{
		Admin:   admin.String(),
		Members: members,
	})
	s.Require().NoError(err)

	groupID = groupRes.GroupId
	groupPolicy := &group.MsgCreateGroupPolicy{
		Admin:   admin.String(),
		GroupId: groupID,
	}

	if policy != nil {
		err = groupPolicy.SetDecisionPolicy(policy)
		s.Require().NoError(err)

		groupPolicyRes, err := s.keeper.CreateGroupPolicy(s.ctx, groupPolicy)
		s.Require().NoError(err)
		policyAddr = groupPolicyRes.Address
	}

	return policyAddr, groupID
}
