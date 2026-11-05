import { AlbumPicker } from "./AlbumPicker";
import { ChannelPicker } from "./ChannelPicker";
import { type Caller, type Place, sharedLibrary } from "./model";
import { PeopleSettings, PersonPicker } from "./People";
import { PhotoPicker } from "./PhotoPicker";
import type { PhotoActions } from "./usePhotoActions";

// ActionDialogs are the dialogs the actions on photos open over the window:
// the person to merge into, the album to add photos to, the photos to add to
// an album, the babies' channels to add photos to, and the libraries' people
// and pets switches.
export function ActionDialogs({ place, caller, actions }: { place: Place; caller: Caller; actions: PhotoActions }) {
  const {
    merging,
    setMerging,
    mergeInto,
    picker,
    setPicker,
    addTo,
    createAndAdd,
    photoPicker,
    setPhotoPicker,
    peopleSettings,
    setPeopleSettings,
    setPeople,
    babies,
    channelPicker,
    setChannelPicker,
    addedToChannel,
  } = actions;
  const shared = sharedLibrary(caller);
  return (
    <>
      {merging && (
        <PersonPicker
          person={merging}
          viewing={place.kind === "person" ? place.viewing : ""}
          onPick={(into) => void mergeInto(into)}
          onClose={() => setMerging(undefined)}
        />
      )}
      {picker && (
        <AlbumPicker
          caller={caller}
          assets={picker}
          onPick={(album) => void addTo(album, picker)}
          onCreate={(library, name) => void createAndAdd(library, name, picker)}
          onClose={() => setPicker(undefined)}
        />
      )}
      {photoPicker && (
        <PhotoPicker
          album={photoPicker}
          onAdd={(assets) => {
            setPhotoPicker(undefined);
            void addTo(photoPicker, assets);
          }}
          onClose={() => setPhotoPicker(undefined)}
        />
      )}
      {channelPicker && shared && (
        <ChannelPicker
          assets={channelPicker}
          babies={babies}
          sharedLibraryId={shared.id}
          onDone={addedToChannel}
          onClose={() => setChannelPicker(undefined)}
        />
      )}
      {peopleSettings && (
        <PeopleSettings
          caller={caller}
          onSet={(library, on) => void setPeople(library, on)}
          onClose={() => setPeopleSettings(false)}
        />
      )}
    </>
  );
}
