ALTER TABLE products
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_products_value_product_id (value DESC, product_id),
    ADD INDEX idx_products_value_desc_product_id (value, product_id),
    ADD INDEX idx_products_weight_desc_product_id (weight DESC, product_id),
    ADD INDEX idx_products_weight_product_id (weight, product_id),
    ADD INDEX idx_products_name_desc_product_id (name DESC, product_id),
    ADD INDEX idx_products_name_product_id (name, product_id),
    ADD INDEX idx_products_name (name),
    ADD INDEX idx_products_product_id_weight_value (product_id, weight, value);

-- ログインの改善
ALTER TABLE users
    ALGORITHM=INPLACE,
    LOCK=NONE,
    ADD INDEX idx_users_user_name (user_name);

ALTER TABLE orders
    ALGORITHM=INPLACE,
    LOCK=NONE,
    -- delivery plan を作るときの orders の join に使う (covering index)
    ADD INDEX idx_orders_shipped_status_product_id_order_id (shipped_status, product_id, order_id),
    ADD INDEX idx_orders_user_id_shipped_status_order_id (user_id, shipped_status, order_id),
    ADD INDEX idx_orders_user_id_created_at (user_id, created_at);
