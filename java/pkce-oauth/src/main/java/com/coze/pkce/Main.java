package com.coze.pkce;

import com.coze.openapi.client.auth.LoadAuthConfig;
import com.coze.openapi.client.auth.OAuthConfig;
import com.coze.openapi.service.auth.PKCEOAuthClient;
import com.coze.pkce.server.TokenServer;

public class Main {
  private static final int PORT = 8080;
  private static final String configFilePath = "coze_oauth_config.json";

  public static void main(String[] args) throws Exception {
    TokenServer server = null;
    try {
      // 加载配置
      OAuthConfig config = OAuthConfig.load(new LoadAuthConfig(configFilePath));

      // 初始化 WEB OAuth 客户端
      PKCEOAuthClient oauth = PKCEOAuthClient.loadFromConfig(new LoadAuthConfig(configFilePath));

      // 启动服务器
      server = new TokenServer(oauth, config);
      server.start(PORT);
      // 保持主线程运行
      Thread.currentThread().join();
    } catch (Exception e) {
      e.printStackTrace();
    } finally {
      server.stop();
    }
  }
}
