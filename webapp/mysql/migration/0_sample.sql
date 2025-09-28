-- このファイルに記述されたSQLコマンドが、マイグレーション時に実行されます。
ALTER TABLE products
    ADD INDEX idx_products_name_product_id (name, product_id),
    ADD INDEX idx_products_name_desc_product_id (name DESC, product_id),
    ADD INDEX idx_products_value_product_id (value, product_id),
    ADD INDEX idx_products_value_desc_product_id (value DESC, product_id),
    ADD INDEX idx_products_weight_product_id (weight, product_id),
    ADD INDEX idx_products_weight_desc_product_id (weight DESC, product_id);
