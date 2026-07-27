ALTER TABLE auction_domain_outbox
  ADD KEY idx_domain_outbox_route_order (topic, partition_key, published_at_ms, id);
