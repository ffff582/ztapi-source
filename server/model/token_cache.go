package model

import (
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func cacheSetToken(token Token) error {
	key, token := prepareTokenCacheEntry(token)
	err := common.RedisHSetObj(fmt.Sprintf("token:%s", key), &token, time.Duration(common.RedisKeyCacheSeconds())*time.Second)
	if err != nil {
		return err
	}
	return nil
}

func prepareTokenCacheEntry(token Token) (string, Token) {
	key := common.GenerateHMAC(token.KeyHash)
	token.Clean()
	return key, token
}

func cacheDeleteToken(keyHash string) error {
	key := common.GenerateHMAC(keyHash)
	err := common.RedisDelKey(fmt.Sprintf("token:%s", key))
	if err != nil {
		return err
	}
	return nil
}

func cacheIncrTokenQuota(keyHash string, increment int64) error {
	key := common.GenerateHMAC(keyHash)
	err := common.RedisHIncrBy(fmt.Sprintf("token:%s", key), constant.TokenFiledRemainQuota, increment)
	if err != nil {
		return err
	}
	return nil
}

func cacheDecrTokenQuota(keyHash string, decrement int64) error {
	return cacheIncrTokenQuota(keyHash, -decrement)
}

func cacheSetTokenField(keyHash string, field string, value string) error {
	key := common.GenerateHMAC(keyHash)
	err := common.RedisHSetField(fmt.Sprintf("token:%s", key), field, value)
	if err != nil {
		return err
	}
	return nil
}

// cacheGetTokenByHash loads a token from Redis using its lookup hash.
func cacheGetTokenByHash(keyHash string) (*Token, error) {
	hmacKey := common.GenerateHMAC(keyHash)
	if !common.RedisEnabled {
		return nil, fmt.Errorf("redis is not enabled")
	}
	var token Token
	err := common.RedisHGetObj(fmt.Sprintf("token:%s", hmacKey), &token)
	if err != nil {
		return nil, err
	}
	token.KeyHash = keyHash
	return &token, nil
}
