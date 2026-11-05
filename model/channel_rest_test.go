package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createMultiKeyRestTestChannel(t *testing.T, keys string) *Channel {
	t.Helper()
	channel := &Channel{
		Name:   "rest-test-" + time.Now().Format("150405.000000"),
		Key:    keys,
		Status: common.ChannelStatusEnabled,
		ChannelInfo: ChannelInfo{
			IsMultiKey:   true,
			MultiKeySize: len(splitTestKeys(keys)),
			MultiKeyMode: constant.MultiKeyModeRandom,
		},
	}
	require.NoError(t, DB.Create(channel).Error)
	t.Cleanup(func() { DB.Unscoped().Delete(&Channel{}, channel.Id) })
	return channel
}

func splitTestKeys(keys string) []string {
	result := []string{}
	start := 0
	for i := 0; i < len(keys); i++ {
		if keys[i] == '\n' {
			result = append(result, keys[start:i])
			start = i + 1
		}
	}
	result = append(result, keys[start:])
	return result
}

func TestMultiKeyRestSkipsAndRestores(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Channel{}))
	channel := createMultiKeyRestTestChannel(t, "key-a\nkey-b\nkey-c")
	now := common.GetTimestamp()

	until, ok, err := RestChannelKey(channel.Id, "key-b", "upstream quota exhausted")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Greater(t, until, now)

	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	assert.Equal(t, common.ChannelStatusAutoDisabled, loaded.ChannelInfo.MultiKeyStatusList[1])
	assert.Equal(t, until, loaded.ChannelInfo.MultiKeyRestUntil[1])

	// The rested key must not be handed out; other keys still are.
	seen := map[string]bool{}
	for i := 0; i < 6; i++ {
		key, index, nextErr := loaded.GetNextEnabledKey()
		require.Nil(t, nextErr)
		seen[key] = true
		assert.NotEqual(t, 1, index)
	}
	assert.True(t, seen["key-a"] || seen["key-c"])

	// Expire the rest and make sure the lazy restore brings the key back.
	loaded.ChannelInfo.MultiKeyRestUntil[1] = now - 1
	require.NoError(t, loaded.saveStatusState())
	restored, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	changed := restored.restoreExpiredKeys(now)
	assert.True(t, changed)
	_, hasStatus := restored.ChannelInfo.MultiKeyStatusList[1]
	assert.False(t, hasStatus)
}

func TestMultiKeyRestLadder(t *testing.T) {
	require.NoError(t, DB.AutoMigrate(&Channel{}))
	channel := createMultiKeyRestTestChannel(t, "key-a\nkey-b")
	now := common.GetTimestamp()

	firstUntil, ok, err := RestChannelKey(channel.Id, "key-a", "quota exhausted")
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, int64(30*60), firstUntil-now, 5)

	// Restore, then rest again within the escalation window -> 2h.
	loaded, err := GetChannelById(channel.Id, true)
	require.NoError(t, err)
	loaded.restoreExpiredKeys(now)
	loaded.ChannelInfo.MultiKeyRestUntil[0] = 0
	require.NoError(t, loaded.saveStatusState())

	secondUntil, ok, err := RestChannelKey(channel.Id, "key-a", "quota exhausted again")
	require.NoError(t, err)
	require.True(t, ok)
	assert.InDelta(t, int64(2*3600), secondUntil-now, 5)
}
