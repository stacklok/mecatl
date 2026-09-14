package redisstore

import "github.com/redis/go-redis/v9"

func (st *Store) testClient() redis.UniversalClient {
	st.clients.mu.Lock()
	defer st.clients.mu.Unlock()
	return st.clients.current.pair.durability.client
}

func (st *Store) testFollowClient() redis.UniversalClient {
	st.clients.mu.Lock()
	defer st.clients.mu.Unlock()
	return st.clients.current.pair.follow.client
}
