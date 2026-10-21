-- 仅用于本地开发/测试容器：允许 campus 用户创建 campus_ 前缀的库（集成测试每个用例一个独立库）。
GRANT ALL PRIVILEGES ON `campus\_%`.* TO 'campus'@'%';
FLUSH PRIVILEGES;
