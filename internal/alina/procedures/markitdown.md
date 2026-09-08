# Document conversion with MarkItDown (optional)

Microsoft's MIT-licensed Python utility converts documents to Markdown for LLM
reading: PDF, DOCX, PPTX, XLSX and other formats. This is extraction, not a
guarantee of faithful page layout, table structure or OCR of scanned PDFs.
Source: https://github.com/microsoft/markitdown
Reviewed 2026-09-09: published version 0.1.7; Python 3.10+.

## Discover first

Use shell to check `command -v markitdown` and `command -v python3` (or `python`
on Termux). If absent from PATH, check a previously recorded workspace virtual
environment. Do not claim a converter or a format works until actually tested.
Read plain text with the native read tool; inspect images with view_image;
use web_fetch for public text pages.

## Convert a local document

Use the installed CLI or the absolute path to the virtual environment's CLI:

```sh
markitdown --extension .pdf < '/absolute/input.pdf' > '/absolute/workspace/output.md'
```

Match the extension to the actual format, quote paths, choose a new output path,
and verify the exit status. Read the Markdown with the native read tool, paging
as needed. Check that substantive content was extracted and report missing
images, tables or scanned text. Keep the original file and its provenance.

Use the shell with network=false. Do not enable plugins, Azure services, image
description APIs, transcription services or remote URLs implicitly. Those are
separate capabilities, may transmit the document, and may need credentials and
additional dependencies. ChatGPT subscription credentials are not an OpenAI API
key for MarkItDown. Large or pathological documents can hit the shell timeout;
do not interpret a partial output file as a successful conversion.

## Optional installation, only with consent

Create a virtual environment inside the workspace, then install only the needed
extras. For example, on a compatible host with Python already installed:

```sh
python3 -m venv '/absolute/workspace/tools/markitdown'
'/absolute/workspace/tools/markitdown/bin/python' -m pip install 'markitdown[pdf,docx]==0.1.7'
```

Declare install=true and network=true for package installation so the harness
can obtain consent. Do not use system-wide pip or install `[all]` by default.
Record the actual command, version, tested formats and limitations in the
procedures index. Installation is not automatic and this guide is not a grant.

## Termux limitation

On the Galaxy A15 (Python 3.14.6), `pip index versions onnxruntime` returned
`No matching distribution found` on 2026-09-09. MarkItDown 0.1.7 depends on
Magika 0.6.x, which requires NumPy and ONNX Runtime. A normal upstream pip install
is therefore currently blocked on this device. Do not bypass dependency checks
or promise Termux compatibility. Other platforms, including BSD, also need an
actual dependency check.

Use a suitable installed format-specific utility instead, such as pdftotext for
text-based PDFs when available. Installing such a utility still requires
consent. A remote converter or a custom Android port would be a separate,
explicit design choice; neither is configured here.
