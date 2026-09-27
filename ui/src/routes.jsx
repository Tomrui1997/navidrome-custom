import React from 'react'
import { Route } from 'react-router-dom'
import Personal from './personal/Personal'
import { Sync } from './syncing'
import { MusicSearch } from './musicsearch'

const routes = [
  <Route exact path="/personal" render={() => <Personal />} key={'personal'} />,
  <Route exact path="/sync" render={() => <Sync />} key={'sync'} />,
  <Route
    exact
    path="/musicsearch"
    render={() => <MusicSearch />}
    key={'musicsearch'}
  />,
]

export default routes
