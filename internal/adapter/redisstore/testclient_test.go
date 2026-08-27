package redisstore

import "github.com/redis/go-redis/v9"

func (st *Store) testClient() redis.UniversalClient {
	st.clients.mu.Lock()
	defer st.clients.mu.Unlock()
	return st.clients.current.client
}
