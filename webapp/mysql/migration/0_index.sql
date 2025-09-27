ALTER TABLE products
    -- products の検索用
    ADD FULLTEXT INDEX ft_idx_products_name_description (name, description) WITH PARSER ngram,
    ADD INDEX idx_name (name);

-- fulltext index は 1 度に 1 つしか作れないので分ける
ALTER TABLE products
    -- orders の検索用
    ADD FULLTEXT INDEX ft_idx_name (name) WITH PARSER ngram;

-- ログインの改善
ALTER TABLE users ADD INDEX idx_users_user_name (user_name);

-- delivery plan を作るときの orders の join に使う (covering index)
ALTER TABLE orders ADD INDEX idx_orders_product_id_shipped_status_order_id (product_id, shipped_status, order_id);