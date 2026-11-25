cask "sql-studio" do
  version "0.3.0"
  sha256 "c9680f07c99bb1cb50cbd57f270571d7b125bb37b111a86b02d0eafef8d4358a"

  url "https://github.com/thennavanr/sql-studio/releases/download/v#{version}/SQL-Studio-macOS-universal.dmg"
  name "SQL Studio"
  desc "Lightweight SQL Server, Azure SQL, MongoDB and Redis client"
  homepage "https://github.com/thennavanr/sql-studio"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: ">= :big_sur"

  app "SQL Studio.app"

  # The app is ad-hoc signed but not notarized. Clear the download quarantine
  # so macOS does not report it as "damaged".
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{appdir}/SQL Studio.app"]
  end

  zap trash: "~/Library/Application Support/SQL Studio"
end
