import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import { Sync } from './syncing'
import { MusicSearch } from './musicsearch'
import { DuplicateList } from './duplicates'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route exact path="/sync" render={() => <Sync />} key={'sync'} />,
  <Route
    exact
    path="/duplicates"
    render={() => <DuplicateList />}
    key={'duplicates'}
  />,
  <Route
    exact
    path="/musicsearch"
    render={() => <MusicSearch />}
    key={'musicsearch'}
  />,
]

export default routes
