# Rekreate

A utility for backing up and restoring books, thumbnails and content catalogue
data (collections, progress) in Kindle devices.

This can be used to transfer the content between different Kindles, as well as
for backing up the current Kindle's content, and restore it after something
like a factory reset.

## Usage

```sh
$ ./rekreate --help
Usage of backup:
  -excludecollection value
        Case-insensitive match of collections to ignore. Can be called
        multiple times.
  -excludepath value
        Glob path of documents to ignore. Can be called multiple times. Wrap
        in single quotes. Used in combination with default paths unless
        disabled, see no-default-path-exclusions
  -no-default-path-exclusions
        Disable default path exclusions. Use excludepath for custom paths

Usage of restore:
  -no-db
        Don't import the content catalogue data
  -no-documents
        Don't import documents
  -no-thumbnails
        Don't import thumbnails
```

### Backing up

This might take some minutes, more depending on how big your library is. Let
it run and don't cancel it. If `WARN` logs are printed, you may ignore them.

With default options, back up everything:

```sh
$ ./rekreate
2026/01/03 23:01:57 Creating temporary export directory...
2026/01/03 23:01:57 Exporting content catalogue partial DB...
2026/01/03 23:02:04 Exporting thumbnails...
2026/01/03 23:02:06 Exporting documents...
2026/01/03 23:04:41 Readying the export...
2026/01/03 23:04:41 Done exporting! Find it in rekreate_20260103-230441.tar.gz
```

You may exclude specific documents from being backed up. You should use glob
expressions to ignore the book _and_ sidecars, use single quotes to avoid
shell expansion:

```sh
# 1. Ignore a whole directory
# 2. Ignore a specific file
# 3. Ignore a specific book and its sidecar (important to keep extension open)
$ ./rekreate \
--excludepath 'documents/Dick, Philip_K/' \
--excludepath 'documents/KUAL.kual' \
--excludepath 'documents/Scott, Lynch/Lies of Locke Lamora, The - Scott Lynch.*'
```

Certain paths are automatically excluded by default, look up
`DefaultExclusions` in the codebase to find the always up to date list, but as
of writing, these are:

```
'/mnt/us/documents/My Clippings*'
'/mnt/us/documents/dictionaries/'
'/mnt/us/documents/Downloads/'
'/mnt/us/documents/*.kol'
'/mnt/us/documents/*.kual'
```

If you don't want these default exclusions, you can pass in
`-no-default-path-exclusions` and pass your own exclusions with
`-excludepath`.

Collections can be excluded with `-excludecollection` option, which can be
passed multiple times. These are a case-insensitive match:

```sh
$ ./rekreate --excludecollection dev --excludecollection 'Gentleman Bastard'
```

All together:

```sh
$ ./rekreate \
--no-default-path-exclusions \
--excludepath 'documents/custom_book.*' --excludepath 'documents/dictionaries/' \
--excludecollection dev --excludecollection sprawl --excludecollection 'abc 1'
```

Once the program has finished, a file like `rekreate_20260103-230441.tar.gz`
will be created in the same path `rekreate` was executed from. Copy this file
over to your target Kindle.

### Restoring

By default everything (books, thumbnails, content catalogue) gets restored.
This might also take some minutes depending on your library size, let it run
until it ends:

```sh
$ ./rekreate restore rekreate_20260103-230441.tar.gz
2026/01/03 22:26:46 Creating temporary import directory...
2026/01/03 22:26:46 Opening backup file...
2026/01/03 22:26:46 Extracting partial DB...
2026/01/03 22:26:46 Backing up existing content catalogue DB...
2026/01/03 22:26:46 Connecting to content catalogue DB...
2026/01/03 22:26:46 Importing content catalogue data...
2026/01/03 22:27:05 Importing thumbnails...
2026/01/03 22:27:22 Importing documents...
2026/01/03 22:29:03 Done importing! Give it some seconds for all the content
to appear in the home screen
```
You can disable each of these parts with one or many of the corresponding
flags. If you pass all of them, then nothing will be imported:

```sh
# Example of only importing the content catalogue data
$ ./rekreate restore --no-documents --no-thumbnails rekreate_20260103-230441.tar.gz
```
