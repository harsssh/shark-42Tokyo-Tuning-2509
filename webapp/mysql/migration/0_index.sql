ALTER TABLE products ADD FULLTEXT INDEX ft_idx_products_name_description (name, description) WITH PARSER ngram;

-- ログインの改善
ALTER TABLE users ADD INDEX idx_users_user_name (user_name);

-- orders の検索のときに name 単体の検索をしたい
ALTER TABLE products ADD FULLTEXT INDEX ft_idx_name (name) WITH PARSER ngram;
ALTER TABLE products ADD INDEX idx_name (name);

-- delivery plan を作るときの orders の join に使う (covering index)
ALTER TABLE orders ADD INDEX idx_orders_product_id_shipped_status_order_id (product_id, shipped_status, order_id);