{
  pkgs,
  flake,
  pname,
}:

pkgs.stdenv.mkDerivation {
  inherit pname;
  version = "0.1.0";

  src = flake;

  buildInputs = [ pkgs.pandoc ];

  buildPhase = ''
    # Create a simple style.css file
    cat > style.css <<EOF
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
      line-height: 1.6;
      max-width: 900px;
      margin: 0 auto;
      padding: 2rem;
      color: #24292e;
    }

    h1, h2, h3, h4, h5, h6 {
      margin-top: 1.5em;
      margin-bottom: 0.5em;
      font-weight: 600;
      line-height: 1.25;
    }

    h1 {
      font-size: 2em;
      border-bottom: 1px solid #eaecef;
      padding-bottom: 0.3em;
    }

    h2 {
      font-size: 1.5em;
      border-bottom: 1px solid #eaecef;
      padding-bottom: 0.3em;
    }

    a {
      color: #0366d6;
      text-decoration: none;
    }

    a:hover {
      text-decoration: underline;
    }

    code {
      font-family: SFMono-Regular, Consolas, "Liberation Mono", Menlo, monospace;
      background-color: rgba(27, 31, 35, 0.05);
      border-radius: 3px;
      padding: 0.2em 0.4em;
      font-size: 85%;
    }

    pre {
      font-family: SFMono-Regular, Consolas, "Liberation Mono", Menlo, monospace;
      background-color: #f6f8fa;
      border-radius: 3px;
      padding: 16px;
      overflow: auto;
    }

    pre code {
      background-color: transparent;
      padding: 0;
    }

    blockquote {
      margin: 0;
      padding: 0 1em;
      color: #6a737d;
      border-left: 0.25em solid #dfe2e5;
    }

    img {
      max-width: 100%;
    }

    .container {
      text-align: center;
    }

    ul, ol {
      padding-left: 2em;
    }

    li {
      margin-top: 0.25em;
    }
    EOF

    # Create HTML version of README
    mkdir -p output
    pandoc -s -f markdown -t html5 \
      --metadata title="FOD Oracle" \
      -o output/index.html \
      README.md \
      --css style.css
      
    # Copy any images from docs folder
    if [ -f docs/sibyl.webp ]; then
      cp docs/sibyl.webp output/
      # Fix image paths in the HTML
      sed -i 's|./docs/sibyl.webp|sibyl.webp|g' output/index.html
    fi
  '';

  installPhase = ''
    mkdir -p $out/share/doc/${pname}
    cp -r output/* $out/share/doc/${pname}/
    cp style.css $out/share/doc/${pname}/
  '';

  meta = {
    description = "HTML documentation for FOD Oracle";
    longDescription = ''
      This package contains the HTML documentation for FOD Oracle,
      generated from the project's README.md using pandoc with
      simple styling.
    '';
  };
}
