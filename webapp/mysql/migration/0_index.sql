ALTER TABLE products
    ALGORITHM=INPLACE,
    -- products の検索用
    ADD FULLTEXT INDEX ft_idx_products_name_description (name, description) WITH PARSER ngram;

-- fulltext index は 1 度に 1 つしか作れないので分ける
ALTER TABLE products
    ALGORITHM=INPLACE,
    -- orders の検索用
    ADD FULLTEXT INDEX ft_idx_name (name) WITH PARSER ngram,
    ADD INDEX idx_name (name);

-- ログインの改善
ALTER TABLE users
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_users_user_name (user_name);

-- delivery plan を作るときの orders の join に使う (covering index)
ALTER TABLE orders
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_orders_product_id_shipped_status_order_id (product_id, shipped_status, order_id);
