package queqiao

import (
	"google.golang.org/protobuf/proto"

	"github.com/xtls/xray-core/common/protocol"
)

type MemoryAccount struct {
	AccountID string
	Devices   []*Device
}

func (a *Account) AsAccount() (protocol.Account, error) {
	return &MemoryAccount{
		AccountID: a.GetAccountId(),
		Devices:   a.GetDevices(),
	}, nil
}

func (a *Account) Equals(another protocol.Account) bool {
	if acc, ok := another.(*Account); ok {
		return a.AccountId == acc.AccountId
	}
	if memAcc, ok := another.(*MemoryAccount); ok {
		return a.AccountId == memAcc.AccountID
	}
	return false
}

func (a *Account) ToProto() proto.Message {
	return a
}

func (a *MemoryAccount) Equals(another protocol.Account) bool {
	if acc, ok := another.(*MemoryAccount); ok {
		return a.AccountID == acc.AccountID
	}
	return false
}

func (a *MemoryAccount) ToProto() proto.Message {
	return &Account{
		AccountId: a.AccountID,
		Devices:   a.Devices,
	}
}

var _ protocol.Account = (*MemoryAccount)(nil)
var _ protocol.AsAccount = (*Account)(nil)
